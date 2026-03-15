package checker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"photo-dedup/internal/config"
	"photo-dedup/internal/db"
	"photo-dedup/internal/hasher"
	"photo-dedup/internal/logging"
	"photo-dedup/internal/progress"
)

// MatchType categorizes how a holding file matched the index.
type MatchType string

const (
	MatchExact MatchType = "exact" // Hamming distance 0 on some rotation pair.
	MatchNear  MatchType = "near"  // Hamming distance 1–threshold.
	MatchNone  MatchType = "none"  // No match found.
	MatchError MatchType = "error" // Error processing the file.
)

// CheckResult records the outcome of checking one holding file.
type CheckResult struct {
	HoldingFile  string    `json:"holding_file"`
	MatchType    MatchType `json:"match_type"`
	MatchPath    string    `json:"match_path,omitempty"`   // path_hint of the matched DB entry
	Distance     int       `json:"distance,omitempty"`     // Hamming distance to best match
	MovedTo      string    `json:"moved_to,omitempty"`     // where the file was moved
	ErrorMessage string    `json:"error_message,omitempty"`
}

// ReviewEntry records info needed for the review UI (written to review/manifest.json).
type ReviewEntry struct {
	ReviewFile   string `json:"review_file"`   // path in review/ folder
	OriginalName string `json:"original_name"` // original filename in holding
	MatchPath    string `json:"match_path"`    // path_hint of the near-match
	Distance     int    `json:"distance"`      // Hamming distance
}

// CheckHoldingFolder scans the holding folder and categorizes files as
// exact duplicates, near matches, or unique.
func CheckHoldingFolder(holdingPath string, database *db.Database, cfg config.Config, logger *logging.Logger) ([]CheckResult, error) {
	duplicatesDir := filepath.Join(holdingPath, "duplicates")
	reviewDir := filepath.Join(holdingPath, "review")

	if !cfg.DryRun {
		if err := os.MkdirAll(duplicatesDir, 0755); err != nil {
			return nil, fmt.Errorf("create duplicates dir: %w", err)
		}
		if err := os.MkdirAll(reviewDir, 0755); err != nil {
			return nil, fmt.Errorf("create review dir: %w", err)
		}
	}

	// Find dcraw if needed.
	dcrawPath := cfg.DcrawPath

	// Enumerate holding folder for image files.
	var files []string
	hasRAW := false

	if cfg.Recursive {
		err := filepath.WalkDir(holdingPath, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil // skip inaccessible entries
			}
			if d.IsDir() {
				// Skip the duplicates/ and review/ output directories.
				name := d.Name()
				if name == "duplicates" || name == "review" {
					return filepath.SkipDir
				}
				return nil
			}
			if config.IsSupportedImage(path) {
				files = append(files, path)
				if config.IsRAW(path) {
					hasRAW = true
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk holding folder: %w", err)
		}
	} else {
		entries, err := os.ReadDir(holdingPath)
		if err != nil {
			return nil, fmt.Errorf("read holding folder: %w", err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			path := filepath.Join(holdingPath, e.Name())
			if config.IsSupportedImage(path) {
				files = append(files, path)
				if config.IsRAW(path) {
					hasRAW = true
				}
			}
		}
	}

	if hasRAW && dcrawPath == "" {
		dcrawPath, _ = hasher.FindDcraw("")
	}

	// Load entire hash index into memory once.
	logger.Info("Loading hash index into memory...")
	hashIdx, err := database.LoadHashIndex()
	if err != nil {
		return nil, fmt.Errorf("load hash index: %w", err)
	}
	logger.Info("Loaded %d hashes into memory", hashIdx.Count())

	logger.Info("Checking %d files in %s (workers=%d)", len(files), holdingPath, cfg.Workers)

	prog := progress.New(int64(len(files)))
	prog.SetLogFunc(logger.Info)
	logger.SetProgress(prog)
	prog.Start()

	// Results are collected in order-preserving slots.
	results := make([]CheckResult, len(files))

	var (
		exactCount int64
		nearCount  int64
		errCount   int64
	)

	// File moves must be serialized to avoid destination collisions.
	type moveRequest struct {
		idx       int
		src       string
		destDir   string
		matchType MatchType
	}
	moveCh := make(chan moveRequest, cfg.Workers*2)
	var moveWg sync.WaitGroup

	// Review entries collected by the move goroutine.
	var reviewMu sync.Mutex
	var reviewEntries []ReviewEntry

	// Single goroutine handles all file moves sequentially.
	moveWg.Add(1)
	go func() {
		defer moveWg.Done()
		for req := range moveCh {
			dest := uniquePath(filepath.Join(req.destDir, filepath.Base(req.src)))
			if err := os.Rename(req.src, dest); err != nil {
				results[req.idx].MatchType = MatchError
				results[req.idx].ErrorMessage = fmt.Sprintf("move file: %v", err)
				results[req.idx].MovedTo = ""
				atomic.AddInt64(&errCount, 1)
			} else {
				results[req.idx].MovedTo = dest
				if req.matchType == MatchNear {
					reviewMu.Lock()
					reviewEntries = append(reviewEntries, ReviewEntry{
						ReviewFile:   dest,
						OriginalName: filepath.Base(req.src),
						MatchPath:    results[req.idx].MatchPath,
						Distance:     results[req.idx].Distance,
					})
					reviewMu.Unlock()
				}
			}
		}
	}()

	// Concurrent hash + match workers.
	workers := cfg.Workers
	if workers <= 0 {
		workers = 4
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	for i, path := range files {
		sem <- struct{}{}
		wg.Add(1)

		go func(idx int, filePath string) {
			defer wg.Done()
			defer func() { <-sem }()
			defer prog.Increment(filePath)

			result := &results[idx]
			result.HoldingFile = filePath

			// Hash the holding file.
			hr := hasher.HashFile(filePath, cfg.HashSize, dcrawPath)
			if hr.Error != nil {
				result.MatchType = MatchError
				result.ErrorMessage = hr.Error.Error()
				atomic.AddInt64(&errCount, 1)
				return
			}

			// Check for exact match (O(1) map lookup).
			if found, pathHint := hashIdx.ExactMatch(hr.DHash0, hr.DHash90, hr.DHash180, hr.DHash270); found {
				result.MatchType = MatchExact
				result.Distance = 0
				result.MatchPath = pathHint
				atomic.AddInt64(&exactCount, 1)

				if !cfg.DryRun {
					moveCh <- moveRequest{idx: idx, src: filePath, destDir: duplicatesDir, matchType: MatchExact}
				}
				return
			}

			// Check for near match (linear scan over pre-decoded bytes).
			if candidate, found := hashIdx.FindMatch(hr.DHash0, hr.DHash90, hr.DHash180, hr.DHash270, cfg.HammingThreshold); found {
				result.MatchType = MatchNear
				result.MatchPath = candidate.PathHint
				result.Distance = candidate.Distance
				atomic.AddInt64(&nearCount, 1)

				if !cfg.DryRun {
					moveCh <- moveRequest{idx: idx, src: filePath, destDir: reviewDir, matchType: MatchNear}
				}
				return
			}

			// No match.
			result.MatchType = MatchNone
		}(i, path)
	}

	wg.Wait()
	close(moveCh)
	moveWg.Wait()

	prog.Finish()
	logger.ClearProgress()

	// Log match results.
	exact := atomic.LoadInt64(&exactCount)
	near := atomic.LoadInt64(&nearCount)
	errs := atomic.LoadInt64(&errCount)
	unique := int64(len(files)) - exact - near - errs

	for _, r := range results {
		switch r.MatchType {
		case MatchExact:
			logger.Debug("  DUPLICATE: %s (exact match with %s)", filepath.Base(r.HoldingFile), r.MatchPath)
		case MatchNear:
			logger.Debug("  REVIEW: %s (distance %d from %s)", filepath.Base(r.HoldingFile), r.Distance, r.MatchPath)
		case MatchError:
			logger.Error("%s — %s", filepath.Base(r.HoldingFile), r.ErrorMessage)
		}
	}

	// Write review manifest for the UI.
	if len(reviewEntries) > 0 && !cfg.DryRun {
		manifestPath := filepath.Join(reviewDir, "manifest.json")
		if err := writeManifest(manifestPath, reviewEntries); err != nil {
			logger.Warn("failed to write review manifest: %v", err)
		}
	}

	logger.Info("Done: %d exact duplicates, %d near matches for review, %d unique, %d errors (out of %d files)",
		exact, near, unique, errs, len(files))

	return results, nil
}

// uniquePath ensures the destination doesn't collide with an existing file
// by appending _1, _2, etc.
func uniquePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	base := path[:len(path)-len(ext)]
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s_%d%s", base, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// writeManifest writes the review entries to a JSON file for the review UI.
func writeManifest(path string, entries []ReviewEntry) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

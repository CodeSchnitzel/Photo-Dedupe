package checker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	ErrorMessage string    `json:"error_message,omitempty"`
}

// CheckHoldingFolder scans the holding folder and categorizes files as
// exact duplicates, near matches, or unique. Files are never moved or
// deleted — results are written to JSON lists in the holding folder.
func CheckHoldingFolder(holdingPath string, database *db.Database, cfg config.Config, logger *logging.Logger) ([]CheckResult, error) {
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
				return
			}

			// Check for near match (linear scan over pre-decoded bytes).
			// Skip cross-format near matches (e.g., .jpg vs .nef) — different
			// file types with similar hashes are not true duplicates.
			if candidate, found := hashIdx.FindMatch(hr.DHash0, hr.DHash90, hr.DHash180, hr.DHash270, cfg.HammingThreshold); found {
				holdingExt := strings.ToLower(filepath.Ext(filePath))
				matchExt := strings.ToLower(filepath.Ext(candidate.PathHint))
				if holdingExt == matchExt {
					result.MatchType = MatchNear
					result.MatchPath = candidate.PathHint
					result.Distance = candidate.Distance
					atomic.AddInt64(&nearCount, 1)
					return
				}
			}

			// No match.
			result.MatchType = MatchNone
		}(i, path)
	}

	wg.Wait()

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

	// Write a single results.json with all matches (exact + near).
	if !cfg.DryRun {
		var matches []CheckResult
		for _, r := range results {
			if r.MatchType == MatchExact || r.MatchType == MatchNear {
				matches = append(matches, r)
			}
		}

		if len(matches) > 0 {
			resultsPath := filepath.Join(holdingPath, "results.json")
			if err := writeResultList(resultsPath, matches); err != nil {
				logger.Warn("failed to write results file: %v", err)
			} else {
				logger.Info("Wrote %s (%d matches)", resultsPath, len(matches))
			}
		}
	}

	logger.Info("Done: %d exact duplicates, %d near matches for review, %d unique, %d errors (out of %d files)",
		exact, near, unique, errs, len(files))

	return results, nil
}

// writeResultList writes a slice of CheckResults to a JSON file.
func writeResultList(path string, results []CheckResult) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

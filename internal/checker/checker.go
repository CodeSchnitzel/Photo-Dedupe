package checker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

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

	// Enumerate holding folder (non-recursive — only top-level files).
	entries, err := os.ReadDir(holdingPath)
	if err != nil {
		return nil, fmt.Errorf("read holding folder: %w", err)
	}

	var files []string
	hasRAW := false
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

	if hasRAW && dcrawPath == "" {
		dcrawPath, _ = hasher.FindDcraw("")
	}

	logger.Info("Checking %d files in %s", len(files), holdingPath)

	prog := progress.New(int64(len(files)))
	prog.SetLogFunc(logger.Info)
	logger.SetProgress(prog)
	prog.Start()

	var results []CheckResult
	var reviewEntries []ReviewEntry

	exactCount := 0
	nearCount := 0

	for _, path := range files {
		result := checkSingleFile(path, database, cfg, dcrawPath, duplicatesDir, reviewDir, logger)
		results = append(results, result)
		prog.Increment(path)

		switch result.MatchType {
		case MatchExact:
			exactCount++
			logger.Info("  DUPLICATE: %s (exact match with %s)", filepath.Base(path), result.MatchPath)
		case MatchNear:
			nearCount++
			logger.Info("  REVIEW: %s (distance %d from %s)", filepath.Base(path), result.Distance, result.MatchPath)
			if !cfg.DryRun {
				reviewEntries = append(reviewEntries, ReviewEntry{
					ReviewFile:   result.MovedTo,
					OriginalName: filepath.Base(path),
					MatchPath:    result.MatchPath,
					Distance:     result.Distance,
				})
			}
		case MatchError:
			logger.Error("%s — %s", filepath.Base(path), result.ErrorMessage)
		}
	}

	prog.Finish()
	logger.ClearProgress()

	// Write review manifest for the UI.
	if len(reviewEntries) > 0 && !cfg.DryRun {
		manifestPath := filepath.Join(reviewDir, "manifest.json")
		if err := writeManifest(manifestPath, reviewEntries); err != nil {
			logger.Warn("failed to write review manifest: %v", err)
		}
	}

	logger.Info("Done: %d exact duplicates, %d near matches for review, %d unique (out of %d files)",
		exactCount, nearCount, len(files)-exactCount-nearCount, len(files))

	return results, nil
}

func checkSingleFile(path string, database *db.Database, cfg config.Config, dcrawPath, duplicatesDir, reviewDir string, logger *logging.Logger) CheckResult {
	result := CheckResult{HoldingFile: path}

	// Hash the holding file.
	hr := hasher.HashFile(path, cfg.HashSize, dcrawPath)
	if hr.Error != nil {
		result.MatchType = MatchError
		result.ErrorMessage = hr.Error.Error()
		return result
	}

	// Check for exact match first (fast path via DB index).
	exactMatch, err := database.ExactMatch(hr.DHash0, hr.DHash90, hr.DHash180, hr.DHash270)
	if err != nil {
		result.MatchType = MatchError
		result.ErrorMessage = fmt.Sprintf("DB exact match query: %v", err)
		return result
	}

	if exactMatch {
		result.MatchType = MatchExact
		result.Distance = 0

		// Get the path hint for display.
		candidates, _ := database.FindCandidates(hr.DHash0, hr.DHash90, hr.DHash180, hr.DHash270, 0)
		if len(candidates) > 0 {
			result.MatchPath = candidates[0].PathHint
		}

		if !cfg.DryRun {
			dest := uniquePath(filepath.Join(duplicatesDir, filepath.Base(path)))
			if err := os.Rename(path, dest); err != nil {
				result.MatchType = MatchError
				result.ErrorMessage = fmt.Sprintf("move to duplicates: %v", err)
				return result
			}
			result.MovedTo = dest
		}
		return result
	}

	// Check for near matches (Hamming distance scan).
	candidates, err := database.FindCandidates(hr.DHash0, hr.DHash90, hr.DHash180, hr.DHash270, cfg.HammingThreshold)
	if err != nil {
		result.MatchType = MatchError
		result.ErrorMessage = fmt.Sprintf("DB candidate search: %v", err)
		return result
	}

	if len(candidates) > 0 {
		// Sort by distance, take the closest match.
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].Distance < candidates[j].Distance
		})
		best := candidates[0]

		result.MatchType = MatchNear
		result.MatchPath = best.PathHint
		result.Distance = best.Distance

		if !cfg.DryRun {
			dest := uniquePath(filepath.Join(reviewDir, filepath.Base(path)))
			if err := os.Rename(path, dest); err != nil {
				result.MatchType = MatchError
				result.ErrorMessage = fmt.Sprintf("move to review: %v", err)
				return result
			}
			result.MovedTo = dest
		}
		return result
	}

	// No match.
	result.MatchType = MatchNone
	return result
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

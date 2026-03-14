package indexer

import (
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

// IndexCollection walks the collection directory, hashes new images, and stores
// them in the database. Already-indexed images (exact hash match) are skipped.
func IndexCollection(collectionPath string, database *db.Database, cfg config.Config, logger *logging.Logger) error {
	// Find dcraw if there are RAW files.
	dcrawPath := cfg.DcrawPath
	hasRAW := false

	// Walk the collection to find all image files.
	var files []string
	err := filepath.WalkDir(collectionPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			logger.Warn("cannot access %s: %v", path, err)
			return nil // Skip inaccessible files, continue walking.
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
		return fmt.Errorf("walk collection: %w", err)
	}

	logger.Info("Found %d image files in %s", len(files), collectionPath)

	// Check dcraw availability if RAW files are present.
	if hasRAW {
		var err error
		dcrawPath, err = hasher.FindDcraw(dcrawPath)
		if err != nil {
			logger.Warn("%v — RAW files will be skipped", err)
		}
	}

	// Process files concurrently.
	workers := cfg.Workers
	if workers <= 0 {
		workers = 4
	}

	var (
		indexed   int64
		skipped   int64
		errors    int64
		batchMu   sync.Mutex
		batch     []db.HashRecord
		batchSize = cfg.BatchSize
	)

	if batchSize <= 0 {
		batchSize = 500
	}

	prog := progress.New(int64(len(files)))
	prog.SetLogFunc(logger.Info)
	logger.SetProgress(prog)
	prog.Start()

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	for _, path := range files {
		sem <- struct{}{} // Acquire semaphore slot.
		wg.Add(1)

		go func(filePath string) {
			defer wg.Done()
			defer func() { <-sem }()            // Release semaphore slot.
			defer prog.Increment(filePath)       // Always count, even on error/skip.

			result := hasher.HashFile(filePath, cfg.HashSize, dcrawPath)
			if result.Error != nil {
				logger.Warn("%v", result.Error)
				atomic.AddInt64(&errors, 1)
				return
			}

			// Check if this hash already exists in the DB (skip if so).
			exists, err := database.ExactMatch(result.DHash0, result.DHash90, result.DHash180, result.DHash270)
			if err != nil {
				logger.Warn("DB lookup failed for %s: %v", filePath, err)
				atomic.AddInt64(&errors, 1)
				return
			}
			if exists {
				atomic.AddInt64(&skipped, 1)
				return
			}

			record := db.HashRecord{
				DHash0:   result.DHash0,
				DHash90:  result.DHash90,
				DHash180: result.DHash180,
				DHash270: result.DHash270,
				PathHint: filePath,
			}

			batchMu.Lock()
			batch = append(batch, record)
			shouldFlush := len(batch) >= batchSize
			var toFlush []db.HashRecord
			if shouldFlush {
				toFlush = batch
				batch = nil
			}
			batchMu.Unlock()

			if shouldFlush {
				if err := database.InsertBatch(toFlush); err != nil {
					logger.Warn("batch insert failed: %v", err)
					atomic.AddInt64(&errors, int64(len(toFlush)))
				} else {
					atomic.AddInt64(&indexed, int64(len(toFlush)))
				}
			}
		}(path)
	}

	wg.Wait()
	prog.Finish()
	logger.ClearProgress()

	// Flush remaining batch.
	if len(batch) > 0 {
		if err := database.InsertBatch(batch); err != nil {
			logger.Warn("final batch insert failed: %v", err)
			atomic.AddInt64(&errors, int64(len(batch)))
		} else {
			atomic.AddInt64(&indexed, int64(len(batch)))
		}
	}

	logger.Info("Indexing complete: %d new, %d already indexed, %d errors (out of %d files)",
		indexed, skipped, errors, len(files))

	return nil
}

// NormalizePath converts a path to use forward slashes and resolves it.
func NormalizePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return strings.ReplaceAll(abs, "\\", "/")
}

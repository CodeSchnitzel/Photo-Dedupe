package cmd

import (
	"photo-dedup/internal/config"

	"github.com/spf13/cobra"
)

var (
	dbPath    string
	workers   int
	hashSize  int
	threshold int
	verbose   bool
	dryRun    bool
	logFile   string
)

var rootCmd = &cobra.Command{
	Use:   "photo-dedup",
	Short: "Perceptual hash-based photo deduplication tool",
	Long: `photo-dedup identifies duplicate photos using perceptual hashing.
It maintains a persistent hash index so that repeat runs are fast.

Workflow:
  1. photo-dedup index <collection-path>   — Build/update the hash index
  2. photo-dedup check <holding-path>      — Find duplicates in the holding folder
  3. photo-dedup review <holding-path>     — Visually review near-matches
  4. photo-dedup stats                     — Show index statistics`,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	defaults := config.DefaultConfig()

	rootCmd.PersistentFlags().StringVar(&dbPath, "db", defaults.DBPath, "path to SQLite database")
	rootCmd.PersistentFlags().IntVar(&workers, "workers", defaults.Workers, "number of parallel workers")
	rootCmd.PersistentFlags().IntVar(&hashSize, "hash-size", defaults.HashSize, "hash dimensions (e.g., 16 for 256-bit)")
	rootCmd.PersistentFlags().IntVar(&threshold, "threshold", defaults.HammingThreshold, "Hamming distance threshold for near-matches")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	rootCmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "report matches without moving files")
	rootCmd.PersistentFlags().StringVar(&logFile, "log-file", "photo-dedup.log", "path to error/warning log file (empty to disable)")
}

// buildConfig creates a Config from the CLI flags.
func buildConfig() config.Config {
	return config.Config{
		DBPath:           dbPath,
		HashSize:         hashSize,
		HammingThreshold: threshold,
		Workers:          workers,
		BatchSize:        500,
		DryRun:           dryRun,
		LogFile:          logFile,
		Verbose:          verbose,
	}
}

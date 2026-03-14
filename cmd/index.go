package cmd

import (
	"fmt"

	"photo-dedup/internal/db"
	"photo-dedup/internal/hasher"
	"photo-dedup/internal/indexer"
	"photo-dedup/internal/logging"

	"github.com/spf13/cobra"
)

var indexCmd = &cobra.Command{
	Use:   "index <collection-path>",
	Short: "Build or update the hash index for a photo collection",
	Long: `Recursively scans the collection directory for image files,
computes perceptual hashes, and stores them in the database.

Already-indexed images are skipped automatically, so re-running
index after adding or moving files is fast.`,
	Args: cobra.ExactArgs(1),
	RunE: runIndex,
}

func init() {
	rootCmd.AddCommand(indexCmd)
}

func runIndex(cmd *cobra.Command, args []string) error {
	collectionPath := args[0]
	cfg := buildConfig()

	logger, err := logging.New(cfg.LogFile, cfg.Verbose)
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer logger.Close()
	defer logger.Summary()

	logger.Info("JPEG decoder: %s", hasher.DecoderName)
	logger.Info("Opening database at %s", cfg.DBPath)
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	logger.Info("Indexing collection at %s (workers=%d, hash_size=%d)",
		collectionPath, cfg.Workers, cfg.HashSize)

	return indexer.IndexCollection(collectionPath, database, cfg, logger)
}

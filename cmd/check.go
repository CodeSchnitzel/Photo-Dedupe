package cmd

import (
	"fmt"

	"photo-dedup/internal/checker"
	"photo-dedup/internal/db"
	"photo-dedup/internal/hasher"
	"photo-dedup/internal/logging"

	"github.com/spf13/cobra"
)

var checkCmd = &cobra.Command{
	Use:   "check <holding-path>",
	Short: "Check holding folder for duplicates against the index",
	Long: `Scans the holding folder for image files and checks each one
against the hash index.

Results:
  - Exact matches are moved to holding/duplicates/
  - Near matches are moved to holding/review/ for visual inspection
  - Unique files are left in place

Use --dry-run to see what would happen without moving any files.`,
	Args: cobra.ExactArgs(1),
	RunE: runCheck,
}

func init() {
	rootCmd.AddCommand(checkCmd)
}

func runCheck(cmd *cobra.Command, args []string) error {
	holdingPath := args[0]
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

	if cfg.DryRun {
		logger.Info("DRY RUN — no files will be moved")
	}

	logger.Info("Checking holding folder at %s (threshold=%d)", holdingPath, cfg.HammingThreshold)

	results, err := checker.CheckHoldingFolder(holdingPath, database, cfg, logger)
	if err != nil {
		return err
	}

	// Summary.
	exact, near, unique, errCount := 0, 0, 0, 0
	for _, r := range results {
		switch r.MatchType {
		case checker.MatchExact:
			exact++
		case checker.MatchNear:
			near++
		case checker.MatchNone:
			unique++
		case checker.MatchError:
			errCount++
		}
	}

	logger.Info("")
	logger.Info("Summary:")
	logger.Info("  Exact duplicates:  %d (moved to duplicates/)", exact)
	logger.Info("  Near matches:      %d (moved to review/)", near)
	logger.Info("  Unique:            %d (left in place)", unique)
	if errCount > 0 {
		logger.Info("  Errors:            %d", errCount)
	}

	return nil
}

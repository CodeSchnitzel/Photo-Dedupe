package cmd

import (
	"fmt"

	"photo-dedup/internal/db"

	"github.com/spf13/cobra"
)

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show hash index statistics",
	RunE:  runStats,
}

func init() {
	rootCmd.AddCommand(statsCmd)
}

func runStats(cmd *cobra.Command, args []string) error {
	cfg := buildConfig()

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	stats, err := database.GetStats()
	if err != nil {
		return fmt.Errorf("get stats: %w", err)
	}

	fmt.Printf("Database: %s\n", cfg.DBPath)
	fmt.Printf("Indexed images: %d\n", stats.TotalImages)
	fmt.Printf("Database size:  %.1f MB\n", float64(stats.DBSizeBytes)/1024/1024)

	return nil
}

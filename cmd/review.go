package cmd

import (
	"photo-dedup/internal/ui"

	"github.com/spf13/cobra"
)

var reviewCmd = &cobra.Command{
	Use:   "review <holding-path>",
	Short: "Launch desktop UI to review near-match duplicates",
	Long: `Opens a desktop window showing side-by-side comparisons of
near-match files listed in results.json (produced by the check command).

For each pair, you can:
  - Keep: not a duplicate — remove from the review list
  - Delete: confirmed duplicate — permanently delete the holding file
  - Skip: move to the next pair without acting`,
	Args: cobra.ExactArgs(1),
	RunE: runReview,
}

func init() {
	rootCmd.AddCommand(reviewCmd)
}

func runReview(cmd *cobra.Command, args []string) error {
	holdingPath := args[0]
	return ui.RunReviewUI(holdingPath)
}

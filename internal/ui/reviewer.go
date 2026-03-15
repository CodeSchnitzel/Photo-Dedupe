package ui

import (
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"os"
	"path/filepath"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"photo-dedup/internal/checker"
)

// RunReviewUI launches the Fyne desktop app for reviewing near-matches.
// It reads review.json from the holding folder (produced by the check command).
// Files are displayed side-by-side; the user can mark them as duplicates or not.
// No files are moved during review — decisions are saved back to the JSON file.
func RunReviewUI(holdingPath string) error {
	resultsPath := filepath.Join(holdingPath, "results.json")
	allEntries, err := loadReviewList(resultsPath)
	if err != nil {
		return fmt.Errorf("load results: %w", err)
	}

	// Filter to near matches only — exact duplicates don't need visual review.
	var entries []checker.CheckResult
	for _, e := range allEntries {
		if e.MatchType == checker.MatchNear {
			entries = append(entries, e)
		}
	}

	if len(entries) == 0 {
		fmt.Println("No near-matches to review.")
		return nil
	}

	a := app.New()
	w := a.NewWindow("Photo Dedup — Review Near Matches")
	w.Resize(fyne.NewSize(1200, 700))

	// State.
	currentIdx := 0
	remaining := make([]checker.CheckResult, len(entries))
	copy(remaining, entries)

	// UI elements.
	statusLabel := widget.NewLabel("")
	distanceLabel := widget.NewLabel("")
	holdingFileLabel := widget.NewLabel("")
	matchFileLabel := widget.NewLabel("")

	holdingImage := canvas.NewImageFromImage(nil)
	holdingImage.FillMode = canvas.ImageFillContain
	holdingImage.SetMinSize(fyne.NewSize(500, 500))

	matchImage := canvas.NewImageFromImage(nil)
	matchImage.FillMode = canvas.ImageFillContain
	matchImage.SetMinSize(fyne.NewSize(500, 500))

	// Update display for current entry.
	updateDisplay := func() {
		if currentIdx >= len(remaining) {
			statusLabel.SetText("All items reviewed!")
			distanceLabel.SetText("")
			holdingFileLabel.SetText("")
			matchFileLabel.SetText("")
			holdingImage.Image = nil
			holdingImage.Refresh()
			matchImage.Image = nil
			matchImage.Refresh()
			return
		}

		entry := remaining[currentIdx]
		statusLabel.SetText(fmt.Sprintf("Item %d of %d", currentIdx+1, len(remaining)))
		distanceLabel.SetText(fmt.Sprintf("Hamming distance: %d", entry.Distance))
		holdingFileLabel.SetText(fmt.Sprintf("New: %s", filepath.Base(entry.HoldingFile)))

		// Load holding image.
		if img, err := loadImage(entry.HoldingFile); err == nil {
			holdingImage.Image = img
		} else {
			holdingImage.Image = nil
			log.Printf("Cannot load holding image: %v", err)
		}
		holdingImage.Refresh()

		// Load match image.
		if entry.MatchPath != "" {
			matchFileLabel.SetText(fmt.Sprintf("Match: %s", filepath.Base(entry.MatchPath)))
			if img, err := loadImage(entry.MatchPath); err == nil {
				matchImage.Image = img
			} else {
				matchImage.Image = nil
				matchFileLabel.SetText(fmt.Sprintf("Match: %s (file not found)", filepath.Base(entry.MatchPath)))
			}
		} else {
			matchFileLabel.SetText("Match: (no path available)")
			matchImage.Image = nil
		}
		matchImage.Refresh()
	}

	// Action: Keep — not a duplicate, remove from review list.
	keepBtn := widget.NewButton("Keep (Not Duplicate)", func() {
		if currentIdx >= len(remaining) {
			return
		}
		entry := remaining[currentIdx]
		log.Printf("KEEP: %s", filepath.Base(entry.HoldingFile))
		remaining = append(remaining[:currentIdx], remaining[currentIdx+1:]...)
		if currentIdx >= len(remaining) && currentIdx > 0 {
			currentIdx = len(remaining) - 1
		}
		updateDisplay()
		saveReviewList(resultsPath, remaining)
	})

	// Action: Delete — confirmed duplicate, delete the holding file.
	deleteBtn := widget.NewButton("Delete (Is Duplicate)", func() {
		if currentIdx >= len(remaining) {
			return
		}
		entry := remaining[currentIdx]

		// Confirm before deleting.
		dialog.ShowConfirm("Confirm Delete",
			fmt.Sprintf("Permanently delete %s?", filepath.Base(entry.HoldingFile)),
			func(confirmed bool) {
				if !confirmed {
					return
				}
				if err := os.Remove(entry.HoldingFile); err != nil {
					dialog.ShowError(fmt.Errorf("delete file: %w", err), w)
					return
				}
				log.Printf("DELETE: %s", entry.HoldingFile)
				remaining = append(remaining[:currentIdx], remaining[currentIdx+1:]...)
				if currentIdx >= len(remaining) && currentIdx > 0 {
					currentIdx = len(remaining) - 1
				}
				updateDisplay()
				saveReviewList(resultsPath, remaining)
			}, w)
	})

	// Action: Skip — move to next without acting.
	skipBtn := widget.NewButton("Skip", func() {
		if currentIdx < len(remaining)-1 {
			currentIdx++
			updateDisplay()
		}
	})

	// Navigation: Previous.
	prevBtn := widget.NewButton("< Prev", func() {
		if currentIdx > 0 {
			currentIdx--
			updateDisplay()
		}
	})

	// Navigation: Next.
	nextBtn := widget.NewButton("Next >", func() {
		if currentIdx < len(remaining)-1 {
			currentIdx++
			updateDisplay()
		}
	})

	// Layout.
	leftPanel := container.NewBorder(
		holdingFileLabel, nil, nil, nil,
		holdingImage,
	)
	rightPanel := container.NewBorder(
		matchFileLabel, nil, nil, nil,
		matchImage,
	)
	imageCompare := container.New(layout.NewGridWrapLayout(fyne.NewSize(550, 550)),
		leftPanel, rightPanel,
	)

	navButtons := container.NewHBox(
		prevBtn, skipBtn, nextBtn,
	)
	actionButtons := container.NewHBox(
		keepBtn, deleteBtn,
	)
	infoBar := container.NewHBox(statusLabel, layout.NewSpacer(), distanceLabel)
	bottomBar := container.NewVBox(
		infoBar,
		container.NewCenter(container.NewHBox(navButtons, layout.NewSpacer(), actionButtons)),
	)

	content := container.NewBorder(
		nil, bottomBar, nil, nil,
		imageCompare,
	)

	w.SetContent(content)
	updateDisplay()
	w.ShowAndRun()

	return nil
}

func loadReviewList(path string) ([]checker.CheckResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var entries []checker.CheckResult
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func saveReviewList(path string, entries []checker.CheckResult) {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		log.Printf("WARNING: failed to save review list: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("WARNING: failed to write review list: %v", err)
	}
}

func loadImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	return img, err
}

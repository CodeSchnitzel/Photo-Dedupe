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

// ReviewEntry mirrors checker.ReviewEntry for JSON deserialization.
type ReviewEntry = checker.ReviewEntry

// RunReviewUI launches the Fyne desktop app for reviewing near-matches.
func RunReviewUI(holdingPath string) error {
	reviewDir := filepath.Join(holdingPath, "review")
	duplicatesDir := filepath.Join(holdingPath, "duplicates")

	// Load manifest.
	manifestPath := filepath.Join(reviewDir, "manifest.json")
	entries, err := loadManifest(manifestPath)
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}

	if len(entries) == 0 {
		fmt.Println("No near-matches to review.")
		return nil
	}

	// Ensure duplicates dir exists.
	os.MkdirAll(duplicatesDir, 0755)

	a := app.New()
	w := a.NewWindow("Photo Dedup — Review Near Matches")
	w.Resize(fyne.NewSize(1200, 700))

	// State.
	currentIdx := 0
	remaining := make([]ReviewEntry, len(entries))
	copy(remaining, entries)

	// UI elements.
	statusLabel := widget.NewLabel("")
	distanceLabel := widget.NewLabel("")
	reviewFileLabel := widget.NewLabel("")
	matchFileLabel := widget.NewLabel("")

	reviewImage := canvas.NewImageFromImage(nil)
	reviewImage.FillMode = canvas.ImageFillContain
	reviewImage.SetMinSize(fyne.NewSize(500, 500))

	matchImage := canvas.NewImageFromImage(nil)
	matchImage.FillMode = canvas.ImageFillContain
	matchImage.SetMinSize(fyne.NewSize(500, 500))

	// Update display for current entry.
	updateDisplay := func() {
		if currentIdx >= len(remaining) {
			statusLabel.SetText("All items reviewed!")
			distanceLabel.SetText("")
			reviewFileLabel.SetText("")
			matchFileLabel.SetText("")
			reviewImage.Image = nil
			reviewImage.Refresh()
			matchImage.Image = nil
			matchImage.Refresh()
			return
		}

		entry := remaining[currentIdx]
		statusLabel.SetText(fmt.Sprintf("Item %d of %d", currentIdx+1, len(remaining)))
		distanceLabel.SetText(fmt.Sprintf("Hamming distance: %d", entry.Distance))
		reviewFileLabel.SetText(fmt.Sprintf("New: %s", entry.OriginalName))

		// Load review image.
		if img, err := loadImage(entry.ReviewFile); err == nil {
			reviewImage.Image = img
		} else {
			reviewImage.Image = nil
			log.Printf("Cannot load review image: %v", err)
		}
		reviewImage.Refresh()

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

	// Action: Keep — move back to holding folder.
	keepBtn := widget.NewButton("Keep (Not Duplicate)", func() {
		if currentIdx >= len(remaining) {
			return
		}
		entry := remaining[currentIdx]
		dest := uniquePath(filepath.Join(holdingPath, entry.OriginalName))
		if err := os.Rename(entry.ReviewFile, dest); err != nil {
			dialog.ShowError(fmt.Errorf("move file: %w", err), w)
			return
		}
		log.Printf("KEEP: %s → %s", entry.OriginalName, dest)
		remaining = append(remaining[:currentIdx], remaining[currentIdx+1:]...)
		if currentIdx >= len(remaining) && currentIdx > 0 {
			currentIdx = len(remaining) - 1
		}
		updateDisplay()
		saveManifest(manifestPath, remaining)
	})

	// Action: Delete — move to duplicates folder.
	deleteBtn := widget.NewButton("Delete (Is Duplicate)", func() {
		if currentIdx >= len(remaining) {
			return
		}
		entry := remaining[currentIdx]
		dest := uniquePath(filepath.Join(duplicatesDir, entry.OriginalName))
		if err := os.Rename(entry.ReviewFile, dest); err != nil {
			dialog.ShowError(fmt.Errorf("move file: %w", err), w)
			return
		}
		log.Printf("DELETE: %s → %s", entry.OriginalName, dest)
		remaining = append(remaining[:currentIdx], remaining[currentIdx+1:]...)
		if currentIdx >= len(remaining) && currentIdx > 0 {
			currentIdx = len(remaining) - 1
		}
		updateDisplay()
		saveManifest(manifestPath, remaining)
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
		reviewFileLabel, nil, nil, nil,
		reviewImage,
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

func loadManifest(path string) ([]ReviewEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var entries []ReviewEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func saveManifest(path string, entries []ReviewEntry) {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		log.Printf("WARNING: failed to save manifest: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("WARNING: failed to write manifest: %v", err)
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

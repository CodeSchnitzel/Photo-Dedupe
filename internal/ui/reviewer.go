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

// tappableImage wraps a canvas.Image so it can receive tap events.
type tappableImage struct {
	widget.BaseWidget
	img     *canvas.Image
	onTap   func()
}

func newTappableImage(img *canvas.Image, onTap func()) *tappableImage {
	t := &tappableImage{img: img, onTap: onTap}
	t.ExtendBaseWidget(t)
	return t
}

func (t *tappableImage) Tapped(_ *fyne.PointEvent) {
	if t.onTap != nil {
		t.onTap()
	}
}

func (t *tappableImage) TappedSecondary(_ *fyne.PointEvent) {}

func (t *tappableImage) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(t.img)
}

func (t *tappableImage) MinSize() fyne.Size {
	return t.img.MinSize()
}

// RunReviewUI launches the Fyne desktop app for reviewing near-matches.
// It reads results.json from the holding folder (produced by the check command).
//
// Interaction model:
//   - Click LEFT image (holding file) to keep it. A dialog asks whether to
//     replace the collection file, delete the collection file, or cancel.
//   - Click RIGHT image (collection file) to keep it. The holding file is deleted.
//   - "Keep Both" leaves both files untouched and removes the entry from review.
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

	// Left side (holding file) labels: path then filename.
	holdingPathLabel := widget.NewLabel("")
	holdingPathLabel.TextStyle = fyne.TextStyle{Italic: true}
	holdingNameLabel := widget.NewLabel("")
	holdingNameLabel.TextStyle = fyne.TextStyle{Bold: true}

	// Right side (collection match) labels: path then filename.
	matchPathLabel := widget.NewLabel("")
	matchPathLabel.TextStyle = fyne.TextStyle{Italic: true}
	matchNameLabel := widget.NewLabel("")
	matchNameLabel.TextStyle = fyne.TextStyle{Bold: true}

	holdingImg := canvas.NewImageFromImage(nil)
	holdingImg.FillMode = canvas.ImageFillContain
	holdingImg.SetMinSize(fyne.NewSize(500, 500))

	matchImg := canvas.NewImageFromImage(nil)
	matchImg.FillMode = canvas.ImageFillContain
	matchImg.SetMinSize(fyne.NewSize(500, 500))

	// Helper to remove current entry and advance.
	removeCurrentAndAdvance := func() {
		remaining = append(remaining[:currentIdx], remaining[currentIdx+1:]...)
		if currentIdx >= len(remaining) && currentIdx > 0 {
			currentIdx = len(remaining) - 1
		}
		saveReviewList(resultsPath, remaining)
	}

	// Forward-declare updateDisplay so the tappable images can reference it.
	var updateDisplay func()

	// Click LEFT image → keep the holding file.
	// Ask: replace collection file with holding file?
	//   Yes → overwrite collection file with holding file
	//   No  → delete collection file, leave holding file
	//   Cancel → do nothing
	onTapHolding := func() {
		if currentIdx >= len(remaining) {
			return
		}
		entry := remaining[currentIdx]
		holdingFile := entry.HoldingFile
		collectionFile := entry.MatchPath

		dlg := dialog.NewConfirm(
			"Keep Holding File",
			fmt.Sprintf("Replace the collection file?\n\n"+
				"Yes = overwrite %s\n      with %s\n\n"+
				"No = delete the collection file,\n      leave the holding file",
				filepath.Base(collectionFile), filepath.Base(holdingFile)),
			func(replace bool) {
				if replace {
					// Copy holding file over collection file.
					data, err := os.ReadFile(holdingFile)
					if err != nil {
						dialog.ShowError(fmt.Errorf("read holding file: %w", err), w)
						return
					}
					if err := os.WriteFile(collectionFile, data, 0644); err != nil {
						dialog.ShowError(fmt.Errorf("overwrite collection file: %w", err), w)
						return
					}
					log.Printf("REPLACE: %s → %s", holdingFile, collectionFile)
				} else {
					// Delete collection file, leave holding file.
					if err := os.Remove(collectionFile); err != nil {
						dialog.ShowError(fmt.Errorf("delete collection file: %w", err), w)
						return
					}
					log.Printf("DELETE COLLECTION: %s (keeping %s)", collectionFile, holdingFile)
				}
				removeCurrentAndAdvance()
				updateDisplay()
			}, w)
		dlg.SetConfirmText("Yes, Replace")
		dlg.SetDismissText("No, Delete Collection")
		dlg.Show()
	}

	// Click RIGHT image → keep the collection file, delete the holding file.
	onTapMatch := func() {
		if currentIdx >= len(remaining) {
			return
		}
		entry := remaining[currentIdx]

		dialog.ShowConfirm("Keep Collection File",
			fmt.Sprintf("Delete the holding file?\n\n%s", filepath.Base(entry.HoldingFile)),
			func(confirmed bool) {
				if !confirmed {
					return
				}
				if err := os.Remove(entry.HoldingFile); err != nil {
					dialog.ShowError(fmt.Errorf("delete holding file: %w", err), w)
					return
				}
				log.Printf("DELETE HOLDING: %s (keeping collection %s)", entry.HoldingFile, entry.MatchPath)
				removeCurrentAndAdvance()
				updateDisplay()
			}, w)
	}

	tappableHolding := newTappableImage(holdingImg, onTapHolding)
	tappableMatch := newTappableImage(matchImg, onTapMatch)

	// Keep Both — not duplicates, remove from review list without deleting anything.
	keepBothBtn := widget.NewButton("Keep Both", func() {
		if currentIdx >= len(remaining) {
			return
		}
		entry := remaining[currentIdx]
		log.Printf("KEEP BOTH: %s and %s", filepath.Base(entry.HoldingFile), filepath.Base(entry.MatchPath))
		removeCurrentAndAdvance()
		updateDisplay()
	})

	updateDisplay = func() {
		if currentIdx >= len(remaining) {
			statusLabel.SetText("All items reviewed!")
			distanceLabel.SetText("")
			holdingPathLabel.SetText("")
			holdingNameLabel.SetText("")
			matchPathLabel.SetText("")
			matchNameLabel.SetText("")
			holdingImg.Image = nil
			holdingImg.Refresh()
			matchImg.Image = nil
			matchImg.Refresh()
			return
		}

		entry := remaining[currentIdx]
		statusLabel.SetText(fmt.Sprintf("Item %d of %d", currentIdx+1, len(remaining)))
		distanceLabel.SetText(fmt.Sprintf("Hamming distance: %d", entry.Distance))

		// Holding file: path + name.
		holdingPathLabel.SetText(filepath.Dir(entry.HoldingFile))
		holdingNameLabel.SetText(fmt.Sprintf("Holding: %s", filepath.Base(entry.HoldingFile)))

		// Load holding image.
		if img, err := loadImage(entry.HoldingFile); err == nil {
			holdingImg.Image = img
		} else {
			holdingImg.Image = nil
			log.Printf("Cannot load holding image: %v", err)
		}
		holdingImg.Refresh()
		tappableHolding.Refresh()

		// Collection match: path + name.
		if entry.MatchPath != "" {
			matchPathLabel.SetText(filepath.Dir(entry.MatchPath))
			matchNameLabel.SetText(fmt.Sprintf("Collection: %s", filepath.Base(entry.MatchPath)))
			if img, err := loadImage(entry.MatchPath); err == nil {
				matchImg.Image = img
			} else {
				matchImg.Image = nil
				matchNameLabel.SetText(fmt.Sprintf("Collection: %s (file not found)", filepath.Base(entry.MatchPath)))
			}
		} else {
			matchPathLabel.SetText("")
			matchNameLabel.SetText("Collection: (no path available)")
			matchImg.Image = nil
		}
		matchImg.Refresh()
		tappableMatch.Refresh()
	}

	// Navigation: Skip.
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
	holdingHeader := container.NewVBox(holdingPathLabel, holdingNameLabel)
	matchHeader := container.NewVBox(matchPathLabel, matchNameLabel)

	leftPanel := container.NewBorder(
		holdingHeader, nil, nil, nil,
		tappableHolding,
	)
	rightPanel := container.NewBorder(
		matchHeader, nil, nil, nil,
		tappableMatch,
	)

	imageCompare := container.New(layout.NewGridWrapLayout(fyne.NewSize(550, 550)),
		leftPanel, rightPanel,
	)

	// "Click to keep" hint + Keep Both centered below images.
	hintLabel := widget.NewLabel("Click an image to keep it")
	hintLabel.Alignment = fyne.TextAlignCenter
	hintLabel.TextStyle = fyne.TextStyle{Italic: true}

	centerRow := container.NewVBox(
		hintLabel,
		container.NewCenter(keepBothBtn),
	)

	navButtons := container.NewHBox(prevBtn, skipBtn, nextBtn)
	infoBar := container.NewHBox(statusLabel, layout.NewSpacer(), distanceLabel)
	bottomBar := container.NewVBox(
		infoBar,
		container.NewCenter(navButtons),
	)

	content := container.NewBorder(
		nil, bottomBar, nil, nil,
		container.NewBorder(nil, centerRow, nil, nil, imageCompare),
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

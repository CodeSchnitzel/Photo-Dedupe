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
	img   *canvas.Image
	onTap func()
}

func newTappableImage(img *canvas.Image, onTap func()) *tappableImage {
	t := &tappableImage{img: img, onTap: onTap}
	t.ExtendBaseWidget(t)
	return t
}

func (t *tappableImage) Tapped(_ *fyne.PointEvent)          { if t.onTap != nil { t.onTap() } }
func (t *tappableImage) TappedSecondary(_ *fyne.PointEvent)  {}
func (t *tappableImage) CreateRenderer() fyne.WidgetRenderer { return widget.NewSimpleRenderer(t.img) }
func (t *tappableImage) MinSize() fyne.Size                  { return t.img.MinSize() }

// RunReviewUI launches the Fyne desktop app for reviewing matches.
// It reads results.json from the holding folder (produced by the check command).
//
// Phase 1 — Exact duplicates: scrollable checklist with batch delete.
// Phase 2 — Near matches: side-by-side comparison with click-to-keep.
func RunReviewUI(holdingPath string) error {
	resultsPath := filepath.Join(holdingPath, "results.json")
	allEntries, err := loadReviewList(resultsPath)
	if err != nil {
		return fmt.Errorf("load results: %w", err)
	}

	var duplicates, nearMatches []checker.CheckResult
	for _, e := range allEntries {
		switch e.MatchType {
		case checker.MatchExact:
			duplicates = append(duplicates, e)
		case checker.MatchNear:
			nearMatches = append(nearMatches, e)
		}
	}

	if len(duplicates) == 0 && len(nearMatches) == 0 {
		fmt.Println("No matches to review.")
		return nil
	}

	a := app.New()
	w := a.NewWindow("Photo Dedup — Review")
	w.Resize(fyne.NewSize(1200, 700))

	// Phase routing: show duplicates first if any, then near matches.
	if len(duplicates) > 0 {
		showDuplicatesPhase(w, resultsPath, duplicates, nearMatches)
	} else {
		showNearMatchPhase(w, resultsPath, nearMatches)
	}

	w.ShowAndRun()
	return nil
}

// showDuplicatesPhase displays a scrollable checklist of exact duplicates.
// All items are checked by default. The user can uncheck any they want to keep,
// then click "Delete Selected" to remove the checked holding files.
func showDuplicatesPhase(w fyne.Window, resultsPath string, duplicates []checker.CheckResult, nearMatches []checker.CheckResult) {
	title := widget.NewLabel(fmt.Sprintf("Exact Duplicates — %d files", len(duplicates)))
	title.TextStyle = fyne.TextStyle{Bold: true}

	subtitle := widget.NewLabel("These holding files have identical hashes to files already in your collection.\nAll are selected for deletion. Uncheck any you want to keep.")
	subtitle.Wrapping = fyne.TextWrapWord

	// Create a check for each duplicate, defaulting to checked.
	checks := make([]*widget.Check, len(duplicates))
	listItems := make([]fyne.CanvasObject, len(duplicates))

	for i, d := range duplicates {
		label := fmt.Sprintf("%s\n  matches: %s",
			filepath.Base(d.HoldingFile),
			filepath.Base(d.MatchPath))
		checks[i] = widget.NewCheck(label, nil)
		checks[i].SetChecked(true)
		listItems[i] = checks[i]
	}

	scrollable := container.NewVScroll(container.NewVBox(listItems...))
	scrollable.SetMinSize(fyne.NewSize(800, 400))

	// Select All / Deselect All.
	selectAllBtn := widget.NewButton("Select All", func() {
		for _, c := range checks {
			c.SetChecked(true)
		}
	})
	deselectAllBtn := widget.NewButton("Deselect All", func() {
		for _, c := range checks {
			c.SetChecked(false)
		}
	})

	var deleteBtn *widget.Button
	deleteBtn = widget.NewButton("Delete Selected", func() {
		// Count selected.
		var selected int
		for _, c := range checks {
			if c.Checked {
				selected++
			}
		}
		if selected == 0 {
			dialog.ShowInformation("Nothing Selected", "No files are selected for deletion.", w)
			return
		}

		dialog.ShowConfirm("Confirm Delete",
			fmt.Sprintf("Permanently delete %d holding file(s)?", selected),
			func(confirmed bool) {
				if !confirmed {
					return
				}
				var deleted, errors int
				var kept []checker.CheckResult
				for i, d := range duplicates {
					if checks[i].Checked {
						if err := os.Remove(d.HoldingFile); err != nil {
							log.Printf("ERROR deleting %s: %v", d.HoldingFile, err)
							errors++
							kept = append(kept, d) // keep in results if delete failed
						} else {
							log.Printf("DELETE: %s", d.HoldingFile)
							deleted++
						}
					} else {
						kept = append(kept, d) // unchecked = user wants to keep
					}
				}

				// Update results.json — remove successfully deleted entries.
				remaining := append(kept, nearMatches...)
				saveReviewList(resultsPath, remaining)

				msg := fmt.Sprintf("Deleted %d file(s).", deleted)
				if errors > 0 {
					msg += fmt.Sprintf("\n%d file(s) could not be deleted (see log).", errors)
				}

				dialog.ShowInformation("Done", msg, w)

				// Move to near-match phase.
				if len(nearMatches) > 0 {
					showNearMatchPhase(w, resultsPath, nearMatches)
				} else {
					w.SetContent(container.NewCenter(widget.NewLabel("All items reviewed!")))
				}
			}, w)
	})

	skipBtn := widget.NewButton("Skip to Near Matches", func() {
		if len(nearMatches) > 0 {
			showNearMatchPhase(w, resultsPath, nearMatches)
		} else {
			w.SetContent(container.NewCenter(widget.NewLabel("No near-matches to review.")))
		}
	})

	topBar := container.NewVBox(title, subtitle)
	selectionBar := container.NewHBox(selectAllBtn, deselectAllBtn)
	bottomBar := container.NewHBox(
		deleteBtn,
		layout.NewSpacer(),
		skipBtn,
	)

	content := container.NewBorder(
		container.NewVBox(topBar, selectionBar),
		bottomBar,
		nil, nil,
		scrollable,
	)

	w.SetContent(content)
}

// showNearMatchPhase displays the side-by-side near-match review UI.
func showNearMatchPhase(w fyne.Window, resultsPath string, nearMatches []checker.CheckResult) {
	w.SetTitle("Photo Dedup — Review Near Matches")

	if len(nearMatches) == 0 {
		w.SetContent(container.NewCenter(widget.NewLabel("No near-matches to review.")))
		return
	}

	// State.
	currentIdx := 0
	remaining := make([]checker.CheckResult, len(nearMatches))
	copy(remaining, nearMatches)

	// UI elements.
	statusLabel := widget.NewLabel("")
	distanceLabel := widget.NewLabel("")

	holdingPathLabel := widget.NewLabel("")
	holdingPathLabel.TextStyle = fyne.TextStyle{Italic: true}
	holdingNameLabel := widget.NewLabel("")
	holdingNameLabel.TextStyle = fyne.TextStyle{Bold: true}

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

	removeCurrentAndAdvance := func() {
		remaining = append(remaining[:currentIdx], remaining[currentIdx+1:]...)
		if currentIdx >= len(remaining) && currentIdx > 0 {
			currentIdx = len(remaining) - 1
		}
		saveReviewList(resultsPath, remaining)
	}

	var updateDisplay func()

	// Click LEFT image → keep the holding file.
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

	// Click RIGHT image → keep the collection file, delete holding file.
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

		holdingPathLabel.SetText(filepath.Dir(entry.HoldingFile))
		holdingNameLabel.SetText(fmt.Sprintf("Holding: %s", filepath.Base(entry.HoldingFile)))

		if img, err := loadImage(entry.HoldingFile); err == nil {
			holdingImg.Image = img
		} else {
			holdingImg.Image = nil
			log.Printf("Cannot load holding image: %v", err)
		}
		holdingImg.Refresh()
		tappableHolding.Refresh()

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

	skipBtn := widget.NewButton("Skip", func() {
		if currentIdx < len(remaining)-1 {
			currentIdx++
			updateDisplay()
		}
	})
	prevBtn := widget.NewButton("< Prev", func() {
		if currentIdx > 0 {
			currentIdx--
			updateDisplay()
		}
	})
	nextBtn := widget.NewButton("Next >", func() {
		if currentIdx < len(remaining)-1 {
			currentIdx++
			updateDisplay()
		}
	})

	// Layout.
	holdingHeader := container.NewVBox(holdingPathLabel, holdingNameLabel)
	matchHeader := container.NewVBox(matchPathLabel, matchNameLabel)

	leftPanel := container.NewBorder(holdingHeader, nil, nil, nil, tappableHolding)
	rightPanel := container.NewBorder(matchHeader, nil, nil, nil, tappableMatch)

	imageCompare := container.New(layout.NewGridWrapLayout(fyne.NewSize(550, 550)),
		leftPanel, rightPanel,
	)

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

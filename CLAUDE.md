# Photo-Dedupe — Claude Code Instructions

## Project Overview

Perceptual hash-based photo deduplication tool. Identifies duplicate photos in a holding folder by comparing against a persistent hash index of an existing collection (~1TB, 200K+ files).

## Build Commands

```bash
cd \\DeepThought\projects\Photo-Dedupe
go mod tidy
go build -o photo-dedup.exe .
```

## Usage

```bash
photo-dedup index <collection-path>    # Build/update hash index
photo-dedup check <holding-path>       # Find duplicates in holding folder
photo-dedup review <holding-path>      # Desktop UI for near-match review
photo-dedup stats                      # Show index statistics
```

Global flags: `--db <path>`, `--workers <n>`, `--threshold <n>`, `--hash-size <n>`, `--dry-run`, `-v`

## Architecture

- **All Go** — single compiled binary, no Python or other runtimes
- **Perceptual hashing:** 256-bit dHash via `goimagehash.ExtDifferenceHash(img, 16, 16)`
- **Rotation handling:** Each image hashed at 0°, 90°, 180°, 270° after EXIF normalization
- **Location-independent index:** DB stores hashes (not file paths as primary key). Paths are stored as display hints only. Moving files doesn't break the index.
- **Two-folder dedup results:** Exact matches → `holding/duplicates/`, near matches → `holding/review/`
- **Review UI:** Fyne desktop app for side-by-side visual comparison of near-matches
- **NEF/RAW support:** Shells out to `dcraw -c -e` to extract embedded JPEG preview
- **Database:** SQLite via `modernc.org/sqlite` (pure Go, no CGo), WAL mode
- **Concurrency:** Goroutine pool with semaphore for parallel image hashing

## Project Structure

```
main.go                          # Entry point
cmd/
  root.go                        # Cobra CLI, global flags
  index.go                       # index subcommand
  check.go                       # check subcommand
  review.go                      # review subcommand (launches Fyne UI)
  stats.go                       # stats subcommand
internal/
  config/config.go               # Config struct, supported extensions, defaults
  db/db.go                       # SQLite schema, batch insert, exact/Hamming search
  hasher/hasher.go               # Image decode, EXIF orientation, dHash at 4 rotations
  hasher/raw.go                  # NEF/RAW decode via dcraw subprocess
  indexer/indexer.go             # Walk collection, skip indexed, concurrent hash + store
  checker/checker.go             # Dedup pipeline: hash → match → categorize → move
  ui/reviewer.go                 # Fyne desktop app for reviewing near-matches
```

## Key Design Decisions

- **Hash size 16x16 = 256-bit:** Higher discriminating power than default 64-bit at 200K+ scale
- **Hamming threshold 10/256:** ~96% bit similarity. Configurable via `--threshold`
- **No SSIM/pixel comparison:** The desktop review UI replaces automated pixel comparison for near-matches
- **DB at `~/.photo-dedup.db` by default:** Single index works across all collection folders
- **Batch inserts (500):** Amortizes transaction overhead during indexing
- **Exact match check before Hamming scan:** Fast path via SQL index, slow path loads all hashes into memory (~50MB for 200K images)

## Dependencies

Direct: `fyne.io/fyne/v2`, `github.com/corona10/goimagehash`, `github.com/rwcarlsen/goexif`, `github.com/spf13/cobra`, `golang.org/x/image`, `modernc.org/sqlite`

Runtime: `dcraw` (only for NEF/RAW files)

## Current Status

All source files written. Needs `go mod tidy` + `go build` to compile. Not yet tested. The go.mod has been resolved with all transitive dependencies.

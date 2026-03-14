package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// SupportedExtensions maps lowercase file extensions to whether they require dcraw.
var SupportedExtensions = map[string]bool{
	".jpg":  false,
	".jpeg": false,
	".png":  false,
	".tiff": false,
	".tif":  false,
	".bmp":  false,
	".gif":  false,
	".nef":  false, // Nikon RAW — requires dcraw
	".cr2":  false, // Canon RAW — requires dcraw
	".arw":  false, // Sony RAW — requires dcraw
	".dng":  false, // Adobe RAW — requires dcraw
}

// RAWExtensions are formats that need dcraw for decoding.
var RAWExtensions = map[string]bool{
	".nef": true,
	".cr2": true,
	".arw": true,
	".dng": true,
}

// Config holds all runtime configuration.
type Config struct {
	DBPath           string
	HashSize         int
	HammingThreshold int
	Workers          int
	BatchSize        int
	DryRun           bool
	DcrawPath        string // path to dcraw binary, empty = auto-detect
	LogFile          string // path to log file, empty = no file logging
	Verbose          bool   // verbose stdout output
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		DBPath:           DefaultDBPath(),
		HashSize:         16, // 16x16 = 256-bit dHash
		HammingThreshold: 10,
		Workers:          runtime.NumCPU(),
		BatchSize:        500,
		DryRun:           false,
	}
}

// DefaultDBPath returns the default database location (~/.photo-dedup.db).
func DefaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".photo-dedup.db")
}

// IsSupportedImage returns true if the file extension is a supported image format.
func IsSupportedImage(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	_, ok := SupportedExtensions[ext]
	return ok
}

// IsRAW returns true if the file extension is a RAW format requiring dcraw.
func IsRAW(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return RAWExtensions[ext]
}

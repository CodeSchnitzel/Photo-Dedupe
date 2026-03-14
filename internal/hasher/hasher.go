package hasher

import (
	"encoding/hex"
	"fmt"
	"image"
	"math/bits"
	"os"

	"github.com/corona10/goimagehash"
	"github.com/rwcarlsen/goexif/exif"
	"photo-dedup/internal/config"
)

// HashResult holds the computed hashes for a single image file.
type HashResult struct {
	Path     string
	DHash0   string // hex hash at 0° rotation
	DHash90  string
	DHash180 string
	DHash270 string
	Error    error
}

// HashFile computes dHash at 4 rotations for the given image file.
// For RAW files, it shells out to dcraw to extract the embedded JPEG preview.
func HashFile(path string, hashSize int, dcrawPath string) HashResult {
	result := HashResult{Path: path}

	var img image.Image
	var err error

	if config.IsRAW(path) {
		img, err = decodeRAW(path, dcrawPath)
	} else {
		img, err = decodeStandard(path)
	}

	if err != nil {
		result.Error = fmt.Errorf("decode %s: %w", path, err)
		return result
	}

	// Apply EXIF orientation correction.
	img = applyEXIFOrientation(path, img)

	// Compute dHash at 4 rotations.
	hashes, err := computeRotatedHashes(img, hashSize)
	if err != nil {
		result.Error = fmt.Errorf("hash %s: %w", path, err)
		return result
	}

	result.DHash0 = hashes[0]
	result.DHash90 = hashes[90]
	result.DHash180 = hashes[180]
	result.DHash270 = hashes[270]
	return result
}

// applyEXIFOrientation reads the EXIF orientation tag and transforms the image.
func applyEXIFOrientation(path string, img image.Image) image.Image {
	f, err := os.Open(path)
	if err != nil {
		return img
	}
	defer f.Close()

	ex, err := exif.Decode(f)
	if err != nil {
		return img // No EXIF data — return as-is.
	}

	tag, err := ex.Get(exif.Orientation)
	if err != nil {
		return img // No orientation tag.
	}

	orient, err := tag.Int(0)
	if err != nil {
		return img
	}

	return transformByOrientation(img, orient)
}

// transformByOrientation applies the EXIF orientation transform.
// See https://exiftool.org/TagNames/EXIF.html for orientation values.
func transformByOrientation(img image.Image, orientation int) image.Image {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	switch orientation {
	case 1:
		return img // Normal — no transform needed.
	case 2:
		return flipHorizontal(img, w, h)
	case 3:
		return rotate180(img, w, h)
	case 4:
		return flipVertical(img, w, h)
	case 5:
		return transpose(img, w, h)
	case 6:
		return rotate90CW(img, w, h)
	case 7:
		return transverse(img, w, h)
	case 8:
		return rotate90CCW(img, w, h)
	default:
		return img
	}
}

// computeRotatedHashes computes extended dHash at 0°, 90°, 180°, 270° rotations.
// Uses ExtDifferenceHash for configurable hash size (e.g., 16x16 = 256-bit).
func computeRotatedHashes(img image.Image, hashSize int) (map[int]string, error) {
	rotations := map[int]image.Image{
		0:   img,
		90:  rotateImage90CW(img),
		180: rotateImage180(img),
		270: rotateImage90CCW(img),
	}

	result := make(map[int]string, 4)
	for deg, rotImg := range rotations {
		hash, err := goimagehash.ExtDifferenceHash(rotImg, hashSize, hashSize)
		if err != nil {
			return nil, fmt.Errorf("dhash at %d°: %w", deg, err)
		}
		result[deg] = extHashToHex(hash)
	}
	return result, nil
}

// extHashToHex converts an ExtImageHash to a hex string for storage.
// The hash is a []uint64 internally; we encode each uint64 as 16 hex chars.
func extHashToHex(h *goimagehash.ExtImageHash) string {
	parts := h.GetHash()
	buf := make([]byte, len(parts)*8)
	for i, v := range parts {
		buf[i*8+0] = byte(v >> 56)
		buf[i*8+1] = byte(v >> 48)
		buf[i*8+2] = byte(v >> 40)
		buf[i*8+3] = byte(v >> 32)
		buf[i*8+4] = byte(v >> 24)
		buf[i*8+5] = byte(v >> 16)
		buf[i*8+6] = byte(v >> 8)
		buf[i*8+7] = byte(v)
	}
	return hex.EncodeToString(buf)
}

// HammingDistanceHex computes the Hamming distance between two hex-encoded hash strings.
func HammingDistanceHex(a, b string) int {
	ba, err1 := hex.DecodeString(a)
	bb, err2 := hex.DecodeString(b)
	if err1 != nil || err2 != nil || len(ba) != len(bb) {
		return 999
	}

	distance := 0
	for i := range ba {
		distance += bits.OnesCount8(ba[i] ^ bb[i])
	}
	return distance
}

// --- Image rotation helpers ---
// These create new NRGBA images with the appropriate pixel mappings.

func newNRGBA(w, h int) *image.NRGBA {
	return image.NewNRGBA(image.Rect(0, 0, w, h))
}

func rotateImage90CW(src image.Image) image.Image {
	b := src.Bounds()
	return rotate90CW(src, b.Dx(), b.Dy())
}

func rotateImage180(src image.Image) image.Image {
	b := src.Bounds()
	return rotate180(src, b.Dx(), b.Dy())
}

func rotateImage90CCW(src image.Image) image.Image {
	b := src.Bounds()
	return rotate90CCW(src, b.Dx(), b.Dy())
}

func rotate90CW(src image.Image, w, h int) image.Image {
	dst := newNRGBA(h, w)
	minX, minY := src.Bounds().Min.X, src.Bounds().Min.Y
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(h-1-y, x, src.At(x+minX, y+minY))
		}
	}
	return dst
}

func rotate180(src image.Image, w, h int) image.Image {
	dst := newNRGBA(w, h)
	minX, minY := src.Bounds().Min.X, src.Bounds().Min.Y
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(w-1-x, h-1-y, src.At(x+minX, y+minY))
		}
	}
	return dst
}

func rotate90CCW(src image.Image, w, h int) image.Image {
	dst := newNRGBA(h, w)
	minX, minY := src.Bounds().Min.X, src.Bounds().Min.Y
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(y, w-1-x, src.At(x+minX, y+minY))
		}
	}
	return dst
}

func flipHorizontal(src image.Image, w, h int) image.Image {
	dst := newNRGBA(w, h)
	minX, minY := src.Bounds().Min.X, src.Bounds().Min.Y
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(w-1-x, y, src.At(x+minX, y+minY))
		}
	}
	return dst
}

func flipVertical(src image.Image, w, h int) image.Image {
	dst := newNRGBA(w, h)
	minX, minY := src.Bounds().Min.X, src.Bounds().Min.Y
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(x, h-1-y, src.At(x+minX, y+minY))
		}
	}
	return dst
}

func transpose(src image.Image, w, h int) image.Image {
	dst := newNRGBA(h, w)
	minX, minY := src.Bounds().Min.X, src.Bounds().Min.Y
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(y, x, src.At(x+minX, y+minY))
		}
	}
	return dst
}

func transverse(src image.Image, w, h int) image.Image {
	dst := newNRGBA(h, w)
	minX, minY := src.Bounds().Min.X, src.Bounds().Min.Y
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(h-1-y, w-1-x, src.At(x+minX, y+minY))
		}
	}
	return dst
}

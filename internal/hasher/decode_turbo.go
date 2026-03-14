//go:build turbo

package hasher

import (
	"image"
	_ "image/gif"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pixiv/go-libjpeg/jpeg"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
)

// DecoderName identifies the active JPEG decoder for logging.
const DecoderName = "libjpeg-turbo"

// decodeStandard opens and decodes a standard image file.
// JPEG files use libjpeg-turbo; other formats fall back to the stdlib.
func decodeStandard(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".jpg" || ext == ".jpeg" {
		img, err := jpeg.Decode(f, &jpeg.DecoderOptions{})
		if err != nil {
			return nil, err
		}
		return img, nil
	}

	// Non-JPEG: fall back to stdlib decoders.
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	return img, nil
}

// decodeJPEG decodes JPEG data from a reader using libjpeg-turbo.
func decodeJPEG(r io.Reader) (image.Image, error) {
	return jpeg.Decode(r, &jpeg.DecoderOptions{})
}

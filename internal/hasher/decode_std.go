//go:build !turbo

package hasher

import (
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
)

// DecoderName identifies the active JPEG decoder for logging.
const DecoderName = "stdlib"

// decodeStandard opens and decodes a standard image file (JPEG, PNG, etc).
func decodeStandard(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	return img, nil
}

// decodeJPEG decodes JPEG data from a reader using the stdlib decoder.
func decodeJPEG(r io.Reader) (image.Image, error) {
	return jpeg.Decode(r)
}

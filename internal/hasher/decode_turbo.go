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
const DecoderName = "libjpeg-turbo (DCT-scaled)"

// dctScaleTarget is the target size for libjpeg's DCT scaling.
// libjpeg picks the smallest native scale factor (1/8, 2/8, ... 8/8)
// that produces output >= this size. For a 24MP (6000×4000) image,
// 1/8 scale = 750×500 — still far more than the 17×17 we need for
// dHash, but eliminates ~98% of IDCT computation and produces a
// much smaller image for the rotation and resize steps.
var dctScaleTarget = image.Rect(0, 0, 256, 256)

// turboOpts are the shared decoder options for JPEG files.
var turboOpts = &jpeg.DecoderOptions{
	ScaleTarget:            dctScaleTarget,
	DisableFancyUpsampling: true,
	DisableBlockSmoothing:  true,
}

// decodeStandard opens and decodes a standard image file.
// JPEG files use libjpeg-turbo with DCT scaling; other formats fall back to the stdlib.
func decodeStandard(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".jpg" || ext == ".jpeg" {
		img, err := jpeg.Decode(f, turboOpts)
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

// decodeJPEG decodes JPEG data from a reader using libjpeg-turbo with DCT scaling.
func decodeJPEG(r io.Reader) (image.Image, error) {
	return jpeg.Decode(r, turboOpts)
}

package hasher

import (
	"bytes"
	"fmt"
	"image"
	"os/exec"
	"runtime"
)

// dcrawDefaultName returns the dcraw binary name for the current OS.
func dcrawDefaultName() string {
	if runtime.GOOS == "windows" {
		return "dcraw.exe"
	}
	return "dcraw"
}

// FindDcraw locates the dcraw binary. Returns the path or an error.
func FindDcraw(customPath string) (string, error) {
	if customPath != "" {
		if _, err := exec.LookPath(customPath); err != nil {
			return "", fmt.Errorf("dcraw not found at %q: %w", customPath, err)
		}
		return customPath, nil
	}

	name := dcrawDefaultName()
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("dcraw not found in PATH — install dcraw to process RAW files: %w", err)
	}
	return path, nil
}

// decodeRAW extracts the embedded JPEG preview from a RAW file using dcraw.
// dcraw -c -e <file> writes the embedded JPEG to stdout.
func decodeRAW(path string, dcrawPath string) (image.Image, error) {
	if dcrawPath == "" {
		var err error
		dcrawPath, err = FindDcraw("")
		if err != nil {
			return nil, err
		}
	}

	cmd := exec.Command(dcrawPath, "-c", "-e", path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("dcraw failed for %s: %w (stderr: %s)", path, err, stderr.String())
	}

	if stdout.Len() == 0 {
		return nil, fmt.Errorf("dcraw produced no output for %s", path)
	}

	img, err := decodeJPEG(&stdout)
	if err != nil {
		return nil, fmt.Errorf("decode dcraw output for %s: %w", path, err)
	}

	return img, nil
}

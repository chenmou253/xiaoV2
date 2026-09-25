package resource

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const WebPQuality = 82

// WebPPath returns a resource path with its image extension changed to .webp.
func WebPPath(relative string) string {
	ext := filepath.Ext(relative)
	if strings.EqualFold(ext, ".webp") {
		return relative
	}
	return strings.TrimSuffix(relative, ext) + ".webp"
}

// ConvertImageToWebP creates a quality-82 WebP image. Existing WebP files are
// validated and copied without another lossy encode.
func ConvertImageToWebP(source, destination string) error {
	if isWebPPath(source) {
		ok, err := validWebP(source)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("invalid WebP image: %s", source)
		}
		sourceAbs, _ := filepath.Abs(source)
		destinationAbs, _ := filepath.Abs(destination)
		if sourceAbs == destinationAbs {
			return nil
		}
		return copyImageFile(source, destination)
	}

	if err := os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".webp-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	if err = temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	defer os.Remove(tempPath)

	output, err := exec.Command("cwebp", "-quiet", "-q", fmt.Sprint(WebPQuality), source, "-o", tempPath).CombinedOutput()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("required command is missing: cwebp (install the webp package)")
		}
		return fmt.Errorf("convert image to WebP (install cwebp): %w: %s", err, strings.TrimSpace(string(output)))
	}
	ok, err := validWebP(tempPath)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("cwebp produced an invalid image for %s", source)
	}
	return os.Rename(tempPath, destination)
}

func isWebPPath(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".webp")
}

func validWebP(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	header := make([]byte, 12)
	if _, err = io.ReadFull(file, header); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return false, nil
		}
		return false, err
	}
	return bytes.Equal(header[:4], []byte("RIFF")) && bytes.Equal(header[8:12], []byte("WEBP")), nil
}

func copyImageFile(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

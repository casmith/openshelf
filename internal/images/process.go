package images

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"

	"golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
	_ "golang.org/x/image/webp"
)

const (
	MaxBytes  = 10 << 20
	maxPixels = 40_000_000
)

var widths = []struct {
	size  Size
	width int
}{{Full, 800}, {Card, 400}, {Thumb, 200}}

var ErrTooLarge = errors.New("image dimensions too large")

// processError means the bytes are not an acceptable image, as opposed to an I/O failure.
type processError struct{ err error }

func (e *processError) Error() string { return e.err.Error() }
func (e *processError) Unwrap() error { return e.err }

func invalid(format string, a ...any) error { return &processError{fmt.Errorf(format, a...)} }

type Processed struct {
	SHA256      string
	ContentType string
	Width       int
	Height      int
}

// Process validates and decodes data, then writes the thumb, card, and full
// JPEGs under dataDir. Identical bytes map to the same files.
func Process(dataDir string, data []byte) (Processed, error) {
	if len(data) > MaxBytes {
		return Processed{}, invalid("image is larger than %d MB", MaxBytes>>20)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Processed{}, invalid("not a supported image: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return Processed{}, invalid("%w: %dx%d", ErrTooLarge, cfg.Width, cfg.Height)
	}
	sum := sha256.Sum256(data)
	p := Processed{SHA256: hex.EncodeToString(sum[:]), ContentType: "image/" + format, Width: cfg.Width, Height: cfg.Height}
	if allExist(dataDir, p.SHA256) {
		return p, nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Processed{}, invalid("decode image: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(FilePath(dataDir, p.SHA256, Full)), 0o755); err != nil {
		return Processed{}, err
	}
	// Each size is scaled from the previous one so only the source and one
	// small RGBA are live at a time.
	cur := src
	for _, s := range widths {
		cur = resize(cur, s.width)
		if err := writeJPEG(FilePath(dataDir, p.SHA256, s.size), cur); err != nil {
			return Processed{}, err
		}
	}
	return p, nil
}

func FilePath(dataDir, sha string, size Size) string {
	return filepath.Join(dataDir, "covers", sha[:2], sha+"-"+string(size)+".jpg")
}

func allExist(dataDir, sha string) bool {
	for _, s := range widths {
		if _, err := os.Stat(FilePath(dataDir, sha, s.size)); err != nil {
			return false
		}
	}
	return true
}

// resize scales src to width w (never upscaling) onto an opaque white RGBA.
// Kernel.Transform avoids the dw*sh scratch buffer that Kernel.Scale allocates.
func resize(src image.Image, w int) *image.RGBA {
	b := src.Bounds()
	if w > b.Dx() {
		w = b.Dx()
	}
	h := max(1, int(float64(b.Dy())*float64(w)/float64(b.Dx())+0.5))
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	sx, sy := float64(w)/float64(b.Dx()), float64(h)/float64(b.Dy())
	s2d := f64.Aff3{sx, 0, -float64(b.Min.X) * sx, 0, sy, -float64(b.Min.Y) * sy}
	draw.CatmullRom.Transform(dst, s2d, src, b, draw.Over, nil)
	return dst
}

func writeJPEG(path string, img image.Image) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 85}); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(f.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

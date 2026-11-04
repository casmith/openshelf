package images

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func testImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	return img
}

func encodePNG(t testing.TB, w, h int) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, testImage(w, h)); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodeFile(t *testing.T, path string) image.Config {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil || format != "jpeg" {
		t.Fatalf("%s: %s %v", path, format, err)
	}
	return cfg
}

func TestProcessSizes(t *testing.T) {
	dir := t.TempDir()
	p, err := Process(dir, encodePNG(t, 1000, 1500))
	if err != nil {
		t.Fatal(err)
	}
	if p.ContentType != "image/png" || p.Width != 1000 || p.Height != 1500 || len(p.SHA256) != 64 {
		t.Fatalf("%+v", p)
	}
	want := map[Size][2]int{Thumb: {200, 300}, Card: {400, 600}, Full: {800, 1200}}
	for s, wh := range want {
		path := FilePath(dir, p.SHA256, s)
		if path != dir+"/covers/"+p.SHA256[:2]+"/"+p.SHA256+"-"+string(s)+".jpg" {
			t.Errorf("path %s", path)
		}
		if cfg := decodeFile(t, path); cfg.Width != wh[0] || cfg.Height != wh[1] {
			t.Errorf("%s: %dx%d", s, cfg.Width, cfg.Height)
		}
	}
}

func TestProcessNeverUpscales(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := gif.Encode(&buf, testImage(300, 450), nil); err != nil {
		t.Fatal(err)
	}
	p, err := Process(dir, buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if p.ContentType != "image/gif" {
		t.Errorf("content type %s", p.ContentType)
	}
	for s, w := range map[Size]int{Full: 300, Card: 300, Thumb: 200} {
		if cfg := decodeFile(t, FilePath(dir, p.SHA256, s)); cfg.Width != w {
			t.Errorf("%s width %d, want %d", s, cfg.Width, w)
		}
	}
}

func TestProcessWebP(t *testing.T) {
	data, err := os.ReadFile("testdata/sample.webp")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Process(t.TempDir(), data)
	if err != nil {
		t.Fatal(err)
	}
	if p.ContentType != "image/webp" || p.Width == 0 {
		t.Errorf("%+v", p)
	}
}

func TestProcessRejects(t *testing.T) {
	dir := t.TempDir()
	if _, err := Process(dir, []byte("<html>not an image</html>")); err == nil {
		t.Error("html accepted")
	}
	// A PNG header claiming 10000x10000 must be rejected before decoding.
	huge := encodePNG(t, 1, 1)
	copy(huge[16:24], []byte{0, 0, 0x27, 0x10, 0, 0, 0x27, 0x10})
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	if _, err := Process(dir, huge); !errors.Is(err, ErrTooLarge) {
		t.Errorf("huge dimensions: %v", err)
	}
	if _, err := Process(dir, make([]byte, MaxBytes+1)); err == nil {
		t.Error("oversized body accepted")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("rejected images wrote files: %v", entries)
	}
}

func TestProcessPeakMemory(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, testImage(4000, 4000), &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	dir := t.TempDir()

	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base := ms.HeapAlloc
	var peak atomic.Uint64
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak.Load() {
				peak.Store(m.HeapAlloc)
			}
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	if _, err := Process(dir, data); err != nil {
		t.Fatal(err)
	}
	close(done)
	<-stopped
	used := int64(peak.Load()) - int64(base)
	t.Logf("peak heap growth %d MB", used>>20)
	if used > 50<<20 {
		t.Errorf("peak heap growth %d MB, want < 50 MB", used>>20)
	}
}

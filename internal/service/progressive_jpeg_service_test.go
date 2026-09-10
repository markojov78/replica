package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"replica/internal/config"
)

func progressiveTestConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{Sharing: config.SharingConfig{ProgressiveLoading: true, ImageStorage: filepath.Join(t.TempDir(), "images"), ImageStorageLimitMB: 1}}
}

func progressiveTestImage(t *testing.T, format string, transparent, animated bool) []byte {
	t.Helper()
	var data bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 5), G: uint8(y * 10), B: 100, A: 255})
		}
	}
	if transparent {
		img.SetNRGBA(0, 0, color.NRGBA{})
	}
	var err error
	switch format {
	case "jpeg":
		err = jpeg.Encode(&data, img, nil)
	case "png":
		err = png.Encode(&data, img)
	case "gif":
		palette := color.Palette{color.Black, color.White}
		if transparent {
			palette[0] = color.Transparent
		}
		frame := image.NewPaletted(img.Bounds(), palette)
		frames := []*image.Paletted{frame}
		delays := []int{0}
		if animated {
			frames = append(frames, frame)
			delays = append(delays, 0)
		}
		err = gif.EncodeAll(&data, &gif.GIF{Image: frames, Delay: delays})
	}
	if err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func paddedPreview(data []byte, size int) []byte {
	out := make([]byte, size)
	copy(out, data)
	return out
}

func progressiveTestRequest(data []byte, name string, opens *atomic.Int32) ProgressiveJPEGRequest {
	return ProgressiveJPEGRequest{FileID: 41, FileVersion: 24, RelativeURI: name, Size: int64(len(data)),
		Open: func(context.Context) (io.ReadCloser, error) {
			opens.Add(1)
			return io.NopCloser(bytes.NewReader(data)), nil
		},
	}
}

func TestProgressiveJPEGConvertsOpaqueStillImagesAndReusesCache(t *testing.T) {
	for _, format := range []string{"jpeg", "png", "gif"} {
		t.Run(format, func(t *testing.T) {
			cfg := progressiveTestConfig(t)
			s := NewProgressiveJPEGService(cfg)
			var opens atomic.Int32
			data := paddedPreview(progressiveTestImage(t, format, false, false), int(ProgressiveJPEGThresholdBytes)+1)
			req := progressiveTestRequest(data, "photo."+format, &opens)
			result, err := s.GetOrCreate(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			defer result.Release()
			if filepath.Base(result.Path) != "41_24.jpg" {
				t.Fatalf("path = %s", result.Path)
			}
			encoded, err := os.ReadFile(result.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(encoded, []byte{0xff, 0xc2}) {
				t.Fatal("missing progressive JPEG SOF2 marker")
			}
			decoded, err := jpeg.Decode(bytes.NewReader(encoded))
			if err != nil || decoded.Bounds().Dx() != 40 || decoded.Bounds().Dy() != 20 {
				t.Fatalf("JPEG dimensions/decode: %v, %v", decoded, err)
			}
			info, err := os.Stat(result.Path)
			if err != nil {
				t.Fatal(err)
			}
			result.Release()
			recreated := NewProgressiveJPEGService(cfg)
			req.Open = func(context.Context) (io.ReadCloser, error) { t.Fatal("cache hit opened source"); return nil, nil }
			cached, err := recreated.GetOrCreate(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			defer cached.Release()
			newInfo, err := os.Stat(cached.Path)
			if err != nil || !info.ModTime().Equal(newInfo.ModTime()) {
				t.Fatal("cache hit changed generation age")
			}
			if opens.Load() != 1 {
				t.Fatalf("source opens = %d", opens.Load())
			}
			// Releasing one reader cannot invalidate another across service recreation.
			s.cache.setLimit(1)
			f, err := os.Open(cached.Path)
			if err != nil {
				t.Fatal(err)
			}
			f.Close()
			cached.Release()
			assertCachePath(t, cached.Path, false)
		})
	}
}

func TestProgressiveJPEGReturnsOriginalForExcludedImages(t *testing.T) {
	for _, test := range []struct {
		name, format          string
		transparent, animated bool
	}{
		{"transparent.png", "png", true, false},
		{"transparent.gif", "gif", true, false},
		{"animated.gif", "gif", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := progressiveTestConfig(t)
			s := NewProgressiveJPEGService(cfg)
			data := paddedPreview(progressiveTestImage(t, test.format, test.transparent, test.animated), int(ProgressiveJPEGThresholdBytes)+1)
			var opens atomic.Int32
			result, err := s.GetOrCreate(context.Background(), progressiveTestRequest(data, test.name, &opens))
			if err != nil {
				t.Fatal(err)
			}
			defer result.Release()
			if result.Path != "" || !bytes.Equal(result.Original, data) || opens.Load() != 1 {
				t.Fatal("excluded image was converted or re-opened")
			}
			files, err := os.ReadDir(cfg.Sharing.ImageStorage)
			if err != nil || len(files) != 0 {
				t.Fatalf("excluded image retained cache files: %v, %v", files, err)
			}
		})
	}
}

func TestProgressiveJPEGThresholdDisabledAndUnsupportedTypes(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled bool
		size    int64
	}{
		{"photo.jpg", false, ProgressiveJPEGThresholdBytes + 1},
		{"photo.jpg", true, ProgressiveJPEGThresholdBytes - 1},
		{"photo.jpg", true, ProgressiveJPEGThresholdBytes},
		{"image.webp", true, ProgressiveJPEGThresholdBytes + 1},
		{"video.mp4", true, ProgressiveJPEGThresholdBytes + 1},
		{"document.pdf", true, ProgressiveJPEGThresholdBytes + 1},
		{"vector.svg", true, ProgressiveJPEGThresholdBytes + 1},
		{"camera.raw", true, ProgressiveJPEGThresholdBytes + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := progressiveTestConfig(t)
			cfg.Sharing.ProgressiveLoading = test.enabled
			s := NewProgressiveJPEGService(cfg)
			result, err := s.GetOrCreate(context.Background(), ProgressiveJPEGRequest{FileID: 1, FileVersion: 1, RelativeURI: test.name, Size: test.size,
				Open: func(context.Context) (io.ReadCloser, error) {
					t.Fatal("excluded source opened by converter")
					return nil, nil
				},
			})
			if err != nil || result.Path != "" || result.Original != nil {
				t.Fatalf("excluded result: %+v, %v", result, err)
			}
			if !test.enabled {
				assertCachePath(t, cfg.Sharing.ImageStorage, false)
			}
		})
	}
}

func TestProgressiveJPEGConcurrentRequestsDeduplicateAndVersionCache(t *testing.T) {
	cfg := progressiveTestConfig(t)
	s := NewProgressiveJPEGService(cfg)
	data := paddedPreview(progressiveTestImage(t, "jpeg", false, false), int(ProgressiveJPEGThresholdBytes)+1)
	var opens atomic.Int32
	req := progressiveTestRequest(data, "photo.jpg", &opens)
	type outcome struct {
		result ProgressiveJPEGResult
		err    error
	}
	results := make(chan outcome, 12)
	for range 12 {
		go func() { result, err := s.GetOrCreate(context.Background(), req); results <- outcome{result, err} }()
	}
	for range 12 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		defer got.result.Release()
	}
	if opens.Load() != 1 {
		t.Fatalf("concurrent source opens = %d", opens.Load())
	}
	req.FileVersion++
	updated, err := s.GetOrCreate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer updated.Release()
	if opens.Load() != 2 || filepath.Base(updated.Path) != "41_25.jpg" {
		t.Fatal("new version reused stale cache")
	}
}

func TestProgressiveJPEGOversizedResultAndGenerationError(t *testing.T) {
	cfg := progressiveTestConfig(t)
	s := NewProgressiveJPEGService(cfg)
	s.cache.setLimit(1)
	data := paddedPreview(progressiveTestImage(t, "jpeg", false, false), int(ProgressiveJPEGThresholdBytes)+1)
	var opens atomic.Int32
	req := progressiveTestRequest(data, "photo.jpg", &opens)
	result, err := s.GetOrCreate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(result.Path) == "41_24.jpg" {
		t.Fatal("oversized JPEG retained under cache name")
	}
	if _, err := os.ReadFile(result.Path); err != nil {
		t.Fatal(err)
	}
	result.Release()
	assertCachePath(t, result.Path, false)
	req.Open = func(context.Context) (io.ReadCloser, error) { return nil, io.ErrUnexpectedEOF }
	if _, err := s.GetOrCreate(context.Background(), req); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("source error = %v", err)
	}
	broken := paddedPreview([]byte("broken"), int(ProgressiveJPEGThresholdBytes)+1)
	if _, err := s.GetOrCreate(context.Background(), progressiveTestRequest(broken, "broken.jpg", &opens)); err == nil {
		t.Fatal("broken image accepted")
	}
	files, err := os.ReadDir(cfg.Sharing.ImageStorage)
	if err != nil || len(files) != 0 {
		t.Fatalf("generation left files: %v, %v", files, err)
	}
}

func TestProgressiveJPEGOrientationAndAnimationDetection(t *testing.T) {
	data := addEXIFOrientation(t, progressiveTestImage(t, "jpeg", false, false), 6)
	data = paddedPreview(data, int(ProgressiveJPEGThresholdBytes)+1)
	var opens atomic.Int32
	s := NewProgressiveJPEGService(progressiveTestConfig(t))
	result, err := s.GetOrCreate(context.Background(), progressiveTestRequest(data, "rotated.jpg", &opens))
	if err != nil {
		t.Fatal(err)
	}
	defer result.Release()
	encoded, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(encoded))
	if err != nil || img.Bounds().Dx() != 20 || img.Bounds().Dy() != 40 {
		t.Fatalf("EXIF orientation not applied: %v", err)
	}
	// An APNG animation-control chunk precedes IDAT. Do not flatten its first frame.
	data = progressiveTestImage(t, "png", false, false)
	chunk := []byte{0, 0, 0, 8, 'a', 'c', 'T', 'L', 0, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(chunk[16:], crc32.ChecksumIEEE(chunk[4:16]))
	data = append(append(append([]byte{}, data[:33]...), chunk...), data[33:]...)
	if img, err := decodeProgressivePNGSource(data); err != nil || img != nil {
		t.Fatalf("animated PNG selection: %v, %v", img, err)
	}
}

func TestProgressiveJPEGStartupEvictionAndInvalidCacheEntry(t *testing.T) {
	cfg := progressiveTestConfig(t)
	if err := os.MkdirAll(cfg.Sharing.ImageStorage, 0o755); err != nil {
		t.Fatal(err)
	}
	old := cacheTestFile(t, cfg.Sharing.ImageStorage, "41_23.jpg", 600_000, 1)
	current := cacheTestFile(t, cfg.Sharing.ImageStorage, "41_24.jpg", 600_000, 2)
	unrelated := cacheTestFile(t, cfg.Sharing.ImageStorage, "photo.jpg", 600_000, 1)
	s := NewProgressiveJPEGService(cfg)
	if s.validateErr != nil {
		t.Fatal(s.validateErr)
	}
	assertCachePath(t, old, false)
	assertCachePath(t, current, true)
	assertCachePath(t, unrelated, true)
	path := filepath.Join(cfg.Sharing.ImageStorage, "99_1.jpg")
	if err := os.Symlink(unrelated, path); err != nil {
		t.Fatal(err)
	}
	_, err := s.GetOrCreate(context.Background(), ProgressiveJPEGRequest{FileID: 99, FileVersion: 1, RelativeURI: "photo.jpg", Size: ProgressiveJPEGThresholdBytes + 1, Open: func(context.Context) (io.ReadCloser, error) {
		t.Fatal("opened source for invalid cache entry")
		return nil, nil
	}})
	if err == nil {
		t.Fatal("cache symlink accepted")
	}
	assertCachePath(t, path, true)
}

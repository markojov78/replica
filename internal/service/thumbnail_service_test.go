package service

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"replica/internal/config"
)

func TestThumbnailFilenameGeneration(t *testing.T) {
	req := ThumbnailRequest{FileID: 125, FileVersion: 4, Size: 256}
	if got := thumbnailFilename(req, ".jpg"); got != "125_4_256.jpg" {
		t.Fatalf("thumbnailFilename jpg = %q, want 125_4_256.jpg", got)
	}
	if got := thumbnailFilename(req, ".svg"); got != "125_4_256.svg" {
		t.Fatalf("thumbnailFilename svg = %q, want 125_4_256.svg", got)
	}
	if got := unknownThumbnailFilename(256); got != "unknown_256.svg" {
		t.Fatalf("unknownThumbnailFilename = %q, want unknown_256.svg", got)
	}
}

func TestThumbnailAllowedSizeValidation(t *testing.T) {
	service := newThumbnailServiceForTest(t)
	if err := service.ValidateSize(256); err != nil {
		t.Fatalf("ValidateSize(256) error = %v", err)
	}
	if err := service.ValidateSize(999); !errors.Is(err, ErrInvalidThumbnailSize) {
		t.Fatalf("ValidateSize(999) error = %v, want ErrInvalidThumbnailSize", err)
	}

	size, err := ParseThumbnailSize("", 128, []int{128, 256})
	if err != nil || size != 128 {
		t.Fatalf("ParseThumbnailSize(default) = %d, %v; want 128, nil", size, err)
	}
	if _, err := ParseThumbnailSize("999", 128, []int{128, 256}); !errors.Is(err, ErrInvalidThumbnailSize) {
		t.Fatalf("ParseThumbnailSize(999) error = %v, want ErrInvalidThumbnailSize", err)
	}
}

func TestGenericSVGGeneration(t *testing.T) {
	svg := genericThumbnailSVG(128, "txt")
	if !bytes.Contains(svg, []byte(`<svg`)) ||
		!bytes.Contains(svg, []byte(`TXT`)) ||
		!bytes.Contains(svg, []byte(`width="128"`)) {
		t.Fatalf("genericThumbnailSVG missing expected content: %s", svg)
	}
	escaped := genericThumbnailSVG(64, `<bad>`)
	if bytes.Contains(escaped, []byte(`<bad>`)) {
		t.Fatalf("genericThumbnailSVG did not escape label: %s", escaped)
	}
}

func TestUnknownSVGGeneration(t *testing.T) {
	service := newThumbnailServiceForTest(t)
	result, err := service.GetOrCreateThumbnail(context.Background(), ThumbnailRequest{
		FileID:      125,
		FileVersion: 4,
		Size:        256,
		RelativeURI: "no-extension",
	})
	if err != nil {
		t.Fatalf("GetOrCreateThumbnail(unknown) error = %v", err)
	}
	if result.ContentType != ThumbnailContentTypeSVG {
		t.Fatalf("ContentType = %q, want %q", result.ContentType, ThumbnailContentTypeSVG)
	}
	if filepath.Base(result.Path) != "unknown_256.svg" {
		t.Fatalf("Path = %q, want unknown_256.svg", result.Path)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("ReadFile(svg) error = %v", err)
	}
	if !bytes.Contains(data, []byte("FILE")) {
		t.Fatalf("unknown svg = %s, want FILE label", data)
	}
}

func TestThumbnailCacheHitReturnsExistingThumbnail(t *testing.T) {
	service := newThumbnailServiceForTest(t)
	source := &countingThumbnailSource{name: "missing.jpg", size: 100, body: []byte("not opened")}
	req := ThumbnailRequest{FileID: 125, FileVersion: 4, Size: 256, RelativeURI: "missing.jpg", Source: source}
	path := service.ThumbnailPath(req, ".jpg")
	if err := os.WriteFile(path, []byte("cached"), 0o644); err != nil {
		t.Fatalf("WriteFile(cache) error = %v", err)
	}

	result, err := service.GetOrCreateThumbnail(context.Background(), req)
	if err != nil {
		t.Fatalf("GetOrCreateThumbnail(cache hit) error = %v", err)
	}
	if result.Path != path || result.ContentType != ThumbnailContentTypeJPEG {
		t.Fatalf("result = %+v, want path=%q content-type=%q", result, path, ThumbnailContentTypeJPEG)
	}
	if source.openCount != 0 {
		t.Fatalf("source opened %d times, want 0", source.openCount)
	}
}

func TestImageThumbnailGeneration(t *testing.T) {
	service := newThumbnailServiceForTest(t)
	sourcePath := filepath.Join(t.TempDir(), "source.png")
	writePNG(t, sourcePath, 64, 32)

	req := ThumbnailRequest{
		FileID:      125,
		FileVersion: 4,
		Size:        256,
		RelativeURI: "source.png",
		Source:      NewLocalFileThumbnailSource(sourcePath, "source.png"),
	}
	result, err := service.GetOrCreateThumbnail(context.Background(), req)
	if err != nil {
		t.Fatalf("GetOrCreateThumbnail(image) error = %v", err)
	}
	if result.ContentType != ThumbnailContentTypeJPEG {
		t.Fatalf("ContentType = %q, want %q", result.ContentType, ThumbnailContentTypeJPEG)
	}
	if filepath.Base(result.Path) != "125_4_256.jpg" {
		t.Fatalf("Path = %q, want 125_4_256.jpg", result.Path)
	}
	file, err := os.Open(result.Path)
	if err != nil {
		t.Fatalf("Open(thumbnail) error = %v", err)
	}
	defer file.Close()
	img, err := jpeg.Decode(file)
	if err != nil {
		t.Fatalf("jpeg.Decode(thumbnail) error = %v", err)
	}
	if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 32 {
		t.Fatalf("thumbnail size = %dx%d, want no upscale 64x32", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

func TestS3ImageThumbnailGeneration(t *testing.T) {
	service := newThumbnailServiceForTest(t)
	sourceData := pngBytes(t, 64, 32)
	client := &fakeS3GetObjectClient{body: sourceData}
	req := ThumbnailRequest{
		FileID:      125,
		FileVersion: 4,
		Size:        256,
		RelativeURI: "source.png",
		Source:      NewS3ThumbnailSource(client, "bucket", "prefix/source.png", "source.png", int64(len(sourceData))),
	}

	result, err := service.GetOrCreateThumbnail(context.Background(), req)
	if err != nil {
		t.Fatalf("GetOrCreateThumbnail(s3 image) error = %v", err)
	}
	if result.ContentType != ThumbnailContentTypeJPEG {
		t.Fatalf("ContentType = %q, want %q", result.ContentType, ThumbnailContentTypeJPEG)
	}
	if client.calls != 1 {
		t.Fatalf("GetObject calls = %d, want 1", client.calls)
	}
	file, err := os.Open(result.Path)
	if err != nil {
		t.Fatalf("Open(thumbnail) error = %v", err)
	}
	defer file.Close()
	img, err := jpeg.Decode(file)
	if err != nil {
		t.Fatalf("jpeg.Decode(thumbnail) error = %v", err)
	}
	if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 32 {
		t.Fatalf("thumbnail size = %dx%d, want no upscale 64x32", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

func TestS3VideoReturnsGenericSVG(t *testing.T) {
	service := newThumbnailServiceForTest(t)
	service.cfg.Sharing.ThumbnailsGenerateForVideo = true
	client := &fakeS3GetObjectClient{body: []byte("not opened")}
	req := ThumbnailRequest{
		FileID:      125,
		FileVersion: 4,
		Size:        256,
		RelativeURI: "clip.mp4",
		Source:      NewS3ThumbnailSource(client, "bucket", "prefix/clip.mp4", "clip.mp4", 1024),
	}

	result, err := service.GetOrCreateThumbnail(context.Background(), req)
	if err != nil {
		t.Fatalf("GetOrCreateThumbnail(s3 video) error = %v", err)
	}
	if result.ContentType != ThumbnailContentTypeSVG {
		t.Fatalf("ContentType = %q, want %q", result.ContentType, ThumbnailContentTypeSVG)
	}
	if client.calls != 0 {
		t.Fatalf("GetObject calls = %d, want 0", client.calls)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("ReadFile(svg) error = %v", err)
	}
	if !bytes.Contains(data, []byte("MP4")) {
		t.Fatalf("s3 video svg = %s, want MP4 label", data)
	}
}

func TestVideoFallbackWhenFFmpegInvalid(t *testing.T) {
	service := newThumbnailServiceForTest(t)
	service.cfg.Sharing.ThumbnailsGenerateForVideo = true
	service.cfg.Sharing.FfmpegPath = filepath.Join(t.TempDir(), "missing-ffmpeg")

	sourcePath := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(sourcePath, []byte("not a real video"), 0o644); err != nil {
		t.Fatalf("WriteFile(video) error = %v", err)
	}

	req := ThumbnailRequest{
		FileID:      125,
		FileVersion: 4,
		Size:        256,
		RelativeURI: "clip.mp4",
		Source:      NewLocalFileThumbnailSource(sourcePath, "clip.mp4"),
	}
	result, err := service.GetOrCreateThumbnail(context.Background(), req)
	if err != nil {
		t.Fatalf("GetOrCreateThumbnail(video fallback) error = %v", err)
	}
	if result.ContentType != ThumbnailContentTypeSVG {
		t.Fatalf("ContentType = %q, want %q", result.ContentType, ThumbnailContentTypeSVG)
	}
	if filepath.Base(result.Path) != "125_4_256.svg" {
		t.Fatalf("Path = %q, want 125_4_256.svg", result.Path)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("ReadFile(svg) error = %v", err)
	}
	if !bytes.Contains(data, []byte("MP4")) {
		t.Fatalf("video fallback svg = %s, want MP4 label", data)
	}
}

func TestVideoThumbnailUsesJPEGTempOutput(t *testing.T) {
	service := newThumbnailServiceForTest(t)
	service.cfg.Sharing.ThumbnailsGenerateForVideo = true
	ffmpegPath := filepath.Join(t.TempDir(), "fake-ffmpeg")
	if err := os.WriteFile(ffmpegPath, []byte(`#!/bin/sh
for last do :; done
case "$last" in
	*.jpg) ;;
	*) echo "missing jpg output suffix: $last" >&2; exit 234 ;;
esac
printf 'jpeg bytes' > "$last"
`), 0o755); err != nil {
		t.Fatalf("WriteFile(fake ffmpeg) error = %v", err)
	}
	service.cfg.Sharing.FfmpegPath = ffmpegPath

	sourcePath := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(sourcePath, []byte("fake video"), 0o644); err != nil {
		t.Fatalf("WriteFile(video) error = %v", err)
	}

	req := ThumbnailRequest{
		FileID:      125,
		FileVersion: 4,
		Size:        256,
		RelativeURI: "clip.mp4",
		Source:      NewLocalFileThumbnailSource(sourcePath, "clip.mp4"),
	}
	result, err := service.GetOrCreateThumbnail(context.Background(), req)
	if err != nil {
		t.Fatalf("GetOrCreateThumbnail(video) error = %v", err)
	}
	if result.ContentType != ThumbnailContentTypeJPEG {
		t.Fatalf("ContentType = %q, want %q", result.ContentType, ThumbnailContentTypeJPEG)
	}
	if filepath.Base(result.Path) != "125_4_256.jpg" {
		t.Fatalf("Path = %q, want 125_4_256.jpg", result.Path)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("ReadFile(thumbnail) error = %v", err)
	}
	if string(data) != "jpeg bytes" {
		t.Fatalf("thumbnail data = %q, want fake ffmpeg output", string(data))
	}
}

func TestImageSourceOverLimitReturnsGenericSVG(t *testing.T) {
	service := newThumbnailServiceForTest(t)
	service.cfg.Sharing.ThumbnailStorageLimitMB = 1
	source := &countingThumbnailSource{
		name: "large.png",
		size: 2 * 1024 * 1024,
		body: pngBytes(t, 64, 32),
	}
	req := ThumbnailRequest{
		FileID:      125,
		FileVersion: 4,
		Size:        256,
		RelativeURI: "large.png",
		Source:      source,
	}

	result, err := service.GetOrCreateThumbnail(context.Background(), req)
	if err != nil {
		t.Fatalf("GetOrCreateThumbnail(large image) error = %v", err)
	}
	if result.ContentType != ThumbnailContentTypeSVG {
		t.Fatalf("ContentType = %q, want %q", result.ContentType, ThumbnailContentTypeSVG)
	}
	if source.openCount != 0 {
		t.Fatalf("source opened %d times, want 0", source.openCount)
	}
}

func TestUnsupportedKnownFileReturnsGenericSVG(t *testing.T) {
	service := newThumbnailServiceForTest(t)
	source := &countingThumbnailSource{name: "document.pdf", size: 100, body: []byte("not opened")}
	req := ThumbnailRequest{
		FileID:      125,
		FileVersion: 4,
		Size:        256,
		RelativeURI: "document.pdf",
		Source:      source,
	}

	result, err := service.GetOrCreateThumbnail(context.Background(), req)
	if err != nil {
		t.Fatalf("GetOrCreateThumbnail(pdf) error = %v", err)
	}
	if result.ContentType != ThumbnailContentTypeSVG {
		t.Fatalf("ContentType = %q, want %q", result.ContentType, ThumbnailContentTypeSVG)
	}
	if source.openCount != 0 {
		t.Fatalf("source opened %d times, want 0", source.openCount)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("ReadFile(svg) error = %v", err)
	}
	if !bytes.Contains(data, []byte("PDF")) {
		t.Fatalf("generic svg = %s, want PDF label", data)
	}
}

func TestAtomicWriteDoesNotLeaveFinalFileOnImageFailure(t *testing.T) {
	service := newThumbnailServiceForTest(t)
	sourcePath := filepath.Join(t.TempDir(), "broken.jpg")
	if err := os.WriteFile(sourcePath, []byte("not an image"), 0o644); err != nil {
		t.Fatalf("WriteFile(source) error = %v", err)
	}
	req := ThumbnailRequest{
		FileID:      125,
		FileVersion: 4,
		Size:        256,
		RelativeURI: "broken.jpg",
		Source:      NewLocalFileThumbnailSource(sourcePath, "broken.jpg"),
	}
	_, err := service.GetOrCreateThumbnail(context.Background(), req)
	if !errors.Is(err, ErrThumbnailImageGeneration) {
		t.Fatalf("GetOrCreateThumbnail(broken image) error = %v, want ErrThumbnailImageGeneration", err)
	}
	if _, statErr := os.Stat(service.ThumbnailPath(req, ".jpg")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("final thumbnail stat error = %v, want not exist", statErr)
	}
}

func TestMissingSourceFile(t *testing.T) {
	service := newThumbnailServiceForTest(t)
	_, err := service.GetOrCreateThumbnail(context.Background(), ThumbnailRequest{
		FileID:      125,
		FileVersion: 4,
		Size:        256,
		RelativeURI: "missing.jpg",
		Source:      NewLocalFileThumbnailSource(filepath.Join(t.TempDir(), "missing.jpg"), "missing.jpg"),
	})
	if !errors.Is(err, ErrThumbnailSourceMissing) {
		t.Fatalf("GetOrCreateThumbnail(missing source) error = %v, want ErrThumbnailSourceMissing", err)
	}
}

func newThumbnailServiceForTest(t *testing.T) *ThumbnailService {
	t.Helper()
	service := NewThumbnailService(config.Config{
		Sharing: config.SharingConfig{
			ThumbnailSizes:             []int{128, 256},
			ThumbnailDefaultSize:       128,
			ThumbnailsGenerateForVideo: false,
			FfmpegPath:                 "ffmpeg",
			ThumbnailStorage:           t.TempDir(),
			ThumbnailStorageLimitMB:    500,
		},
	})
	if err := service.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	return service
}

func writePNG(t *testing.T, path string, width int, height int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 180, A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(png) error = %v", err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
}

func pngBytes(t *testing.T, width int, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 180, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	return buf.Bytes()
}

func TestGenericLabelTruncatesAndNormalizes(t *testing.T) {
	if got := genericLabel(".document"); got != "DOCUMENT" {
		t.Fatalf("genericLabel = %q, want DOCUMENT", got)
	}
	if got := genericLabel(strings.Repeat("a", 20)); got != "AAAAAAAA" {
		t.Fatalf("genericLabel long = %q, want AAAAAAAA", got)
	}
}

type countingThumbnailSource struct {
	name      string
	size      int64
	body      []byte
	openCount int
}

func (s *countingThumbnailSource) Open(ctx context.Context) (io.ReadCloser, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	s.openCount++
	return io.NopCloser(bytes.NewReader(s.body)), nil
}

func (s *countingThumbnailSource) Name() string {
	return s.name
}

func (s *countingThumbnailSource) Size() int64 {
	return s.size
}

func (s *countingThumbnailSource) IsLocalFile() bool {
	return false
}

func (s *countingThumbnailSource) LocalPath() string {
	return ""
}

type fakeS3GetObjectClient struct {
	body  []byte
	calls int
}

func (c *fakeS3GetObjectClient) GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	c.calls++
	return &s3.GetObjectOutput{
		Body: io.NopCloser(bytes.NewReader(c.body)),
	}, nil
}

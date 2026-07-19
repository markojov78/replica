package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"image"
	"image/jpeg"
	_ "image/png"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/disintegration/imaging"

	"replica/internal/config"

	xdraw "golang.org/x/image/draw"
)

const (
	ThumbnailContentTypeJPEG = "image/jpeg"
	ThumbnailContentTypeSVG  = "image/svg+xml"
)

var (
	ErrThumbnailSourceMissing    = errors.New("thumbnail source file missing")
	ErrThumbnailStorage          = errors.New("thumbnail storage problem")
	ErrThumbnailImageGeneration  = errors.New("thumbnail image generation failed")
	ErrInvalidThumbnailSize      = errors.New("invalid thumbnail size")
	ErrInvalidThumbnailRequest   = errors.New("invalid thumbnail request")
	defaultThumbnailVideoTimeout = 10 * time.Second
	defaultThumbnailJPEGQuality  = 85
)

type ThumbnailRequest struct {
	FileID      uint
	FileVersion uint
	Size        int
	RelativeURI string
	Source      ThumbnailSource
}

type ThumbnailResult struct {
	Path        string
	ContentType string
}

type ThumbnailService struct {
	cfg          config.Config
	storageDir   string
	validateErr  error
	videoTimeout time.Duration

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewThumbnailService(cfg config.Config) *ThumbnailService {
	service := &ThumbnailService{
		cfg:          cfg,
		storageDir:   strings.TrimSpace(cfg.Sharing.ThumbnailStorage),
		videoTimeout: defaultThumbnailVideoTimeout,
		locks:        make(map[string]*sync.Mutex),
	}
	service.validateErr = service.validateStorage()
	return service
}

func (s *ThumbnailService) Validate() error {
	if s.validateErr != nil {
		return s.validateErr
	}
	return s.validateStorage()
}

func (s *ThumbnailService) GetOrCreateThumbnail(ctx context.Context, req ThumbnailRequest) (ThumbnailResult, error) {
	if err := s.ValidateSize(req.Size); err != nil {
		return ThumbnailResult{}, err
	}
	if req.FileID == 0 || req.FileVersion == 0 {
		return ThumbnailResult{}, ErrInvalidThumbnailRequest
	}
	if err := s.Validate(); err != nil {
		return ThumbnailResult{}, err
	}

	kind, label := thumbnailFileKind(req.RelativeURI, thumbnailSourceName(req.Source))
	switch kind {
	case thumbnailKindImage:
		if result, ok := s.cachedJPG(req); ok {
			return result, nil
		}
		return s.getOrCreateJPG(ctx, req, s.generateImageThumbnail)
	case thumbnailKindVideo:
		if result, ok := s.cachedJPG(req); ok {
			return result, nil
		}
		if req.Source == nil || !req.Source.IsLocalFile() {
			return s.getOrCreateGenericSVG(req, label)
		}
		if !s.cfg.Sharing.ThumbnailsGenerateForVideo {
			return s.getOrCreateGenericSVG(req, label)
		}
		return s.getOrCreateVideoThumbnail(ctx, req, label)
	case thumbnailKindGeneric:
		return s.getOrCreateGenericSVG(req, label)
	default:
		return s.getOrCreateUnknownSVG(req.Size)
	}
}

func (s *ThumbnailService) cachedJPG(req ThumbnailRequest) (ThumbnailResult, bool) {
	path := s.ThumbnailPath(req, ".jpg")
	if !fileExists(path) {
		return ThumbnailResult{}, false
	}
	return ThumbnailResult{Path: path, ContentType: ThumbnailContentTypeJPEG}, true
}

func (s *ThumbnailService) ValidateSize(size int) error {
	if size <= 0 || !slices.Contains(s.cfg.Sharing.ThumbnailSizes, size) {
		return ErrInvalidThumbnailSize
	}
	return nil
}

func (s *ThumbnailService) ThumbnailPath(req ThumbnailRequest, extension string) string {
	return filepath.Join(s.storageDir, thumbnailFilename(req, extension))
}

func (s *ThumbnailService) UnknownThumbnailPath(size int) string {
	return filepath.Join(s.storageDir, unknownThumbnailFilename(size))
}

func (s *ThumbnailService) validateStorage() error {
	if strings.TrimSpace(s.storageDir) == "" {
		return fmt.Errorf("%w: sharing.thumbnail_storage is required", ErrThumbnailStorage)
	}
	if err := os.MkdirAll(s.storageDir, 0o755); err != nil {
		return fmt.Errorf("%w: %v", ErrThumbnailStorage, err)
	}
	info, err := os.Stat(s.storageDir)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrThumbnailStorage, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrThumbnailStorage, s.storageDir)
	}
	probe, err := os.CreateTemp(s.storageDir, ".thumbnail-probe-*")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrThumbnailStorage, err)
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("%w: %v", ErrThumbnailStorage, err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("%w: %v", ErrThumbnailStorage, err)
	}
	return nil
}

func validateLocalThumbnailSource(source ThumbnailSource) error {
	if source == nil || strings.TrimSpace(source.LocalPath()) == "" {
		return ErrThumbnailSourceMissing
	}
	info, err := os.Stat(source.LocalPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrThumbnailSourceMissing
		}
		return err
	}
	if info.IsDir() {
		return ErrThumbnailSourceMissing
	}
	return nil
}

func (s *ThumbnailService) getOrCreateJPG(ctx context.Context, req ThumbnailRequest, generate func(context.Context, ThumbnailRequest, string) error) (ThumbnailResult, error) {
	path := s.ThumbnailPath(req, ".jpg")
	if fileExists(path) {
		return ThumbnailResult{Path: path, ContentType: ThumbnailContentTypeJPEG}, nil
	}

	unlock := s.lockPath(path)
	defer unlock()
	if fileExists(path) {
		return ThumbnailResult{Path: path, ContentType: ThumbnailContentTypeJPEG}, nil
	}
	if err := generate(ctx, req, path); err != nil {
		return ThumbnailResult{}, err
	}
	return ThumbnailResult{Path: path, ContentType: ThumbnailContentTypeJPEG}, nil
}

func (s *ThumbnailService) getOrCreateVideoThumbnail(ctx context.Context, req ThumbnailRequest, label string) (ThumbnailResult, error) {
	path := s.ThumbnailPath(req, ".jpg")
	if fileExists(path) {
		return ThumbnailResult{Path: path, ContentType: ThumbnailContentTypeJPEG}, nil
	}

	unlock := s.lockPath(path)
	defer unlock()
	if fileExists(path) {
		return ThumbnailResult{Path: path, ContentType: ThumbnailContentTypeJPEG}, nil
	}
	if err := s.generateVideoThumbnail(ctx, req, path); err != nil {
		log.Printf("thumbnail video generation failed file_id=%d version=%d source=%s: %v", req.FileID, req.FileVersion, thumbnailSourceName(req.Source), err)
		return s.getOrCreateGenericSVG(req, label)
	}
	return ThumbnailResult{Path: path, ContentType: ThumbnailContentTypeJPEG}, nil
}

func (s *ThumbnailService) getOrCreateGenericSVG(req ThumbnailRequest, label string) (ThumbnailResult, error) {
	path := s.ThumbnailPath(req, ".svg")
	if fileExists(path) {
		return ThumbnailResult{Path: path, ContentType: ThumbnailContentTypeSVG}, nil
	}

	unlock := s.lockPath(path)
	defer unlock()
	if fileExists(path) {
		return ThumbnailResult{Path: path, ContentType: ThumbnailContentTypeSVG}, nil
	}
	if err := s.writeGenericSVGAtomic(path, req.Size, label); err != nil {
		return ThumbnailResult{}, err
	}
	return ThumbnailResult{Path: path, ContentType: ThumbnailContentTypeSVG}, nil
}

func (s *ThumbnailService) getOrCreateUnknownSVG(size int) (ThumbnailResult, error) {
	path := s.UnknownThumbnailPath(size)
	if fileExists(path) {
		return ThumbnailResult{Path: path, ContentType: ThumbnailContentTypeSVG}, nil
	}

	unlock := s.lockPath(path)
	defer unlock()
	if fileExists(path) {
		return ThumbnailResult{Path: path, ContentType: ThumbnailContentTypeSVG}, nil
	}
	if err := s.writeGenericSVGAtomic(path, size, "FILE"); err != nil {
		return ThumbnailResult{}, err
	}
	return ThumbnailResult{Path: path, ContentType: ThumbnailContentTypeSVG}, nil
}

func (s *ThumbnailService) generateImageThumbnail(ctx context.Context, req ThumbnailRequest, finalPath string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if req.Source == nil {
		return ErrThumbnailSourceMissing
	}
	source, err := req.Source.Open(ctx)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrThumbnailSourceMissing
		}
		return fmt.Errorf("%w: %v", ErrThumbnailImageGeneration, err)
	}
	defer source.Close()

	img, err := imaging.Decode(source, imaging.AutoOrientation(true))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrThumbnailImageGeneration, err)
	}

	thumbnail := resizeToFit(img, req.Size)
	return writeAtomic(s.storageDir, finalPath, func(tmp *os.File) error {
		return jpeg.Encode(tmp, thumbnail, &jpeg.Options{Quality: defaultThumbnailJPEGQuality})
	})
}

func (s *ThumbnailService) generateVideoThumbnail(ctx context.Context, req ThumbnailRequest, finalPath string) error {
	if err := validateLocalThumbnailSource(req.Source); err != nil {
		return err
	}
	ffmpegPath := strings.TrimSpace(s.cfg.Sharing.FfmpegPath)
	if ffmpegPath == "" {
		return errors.New("ffmpeg path is not configured")
	}
	timeout := s.videoTimeout
	if timeout <= 0 {
		timeout = defaultThumbnailVideoTimeout
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tmp, err := os.CreateTemp(s.storageDir, ".thumbnail-*.jpg")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrThumbnailStorage, err)
	}
	tmpName := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("%w: %v", ErrThumbnailStorage, err)
	}
	defer func() {
		_ = os.Remove(tmpName)
	}()

	cmd := exec.CommandContext(cmdCtx, ffmpegPath,
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-ss", "00:00:01",
		"-i", req.Source.LocalPath(),
		"-frames:v", "1",
		"-vf", fmt.Sprintf("scale='min(%d,iw)':-1:force_original_aspect_ratio=decrease", req.Size),
		"-q:v", "3",
		tmpName,
	)
	output, err := cmd.CombinedOutput()
	if cmdCtx.Err() != nil {
		return cmdCtx.Err()
	}
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		if fileExists(finalPath) {
			return nil
		}
		return fmt.Errorf("%w: %v", ErrThumbnailStorage, err)
	}
	return nil
}

func (s *ThumbnailService) writeGenericSVGAtomic(path string, size int, label string) error {
	return writeAtomic(s.storageDir, path, func(tmp *os.File) error {
		_, err := tmp.Write(genericThumbnailSVG(size, label))
		return err
	})
}

func (s *ThumbnailService) lockPath(path string) func() {
	s.mu.Lock()
	lock := s.locks[path]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[path] = lock
	}
	s.mu.Unlock()

	lock.Lock()
	return func() {
		lock.Unlock()
	}
}

// actual thumbnail generating function
func resizeToFit(src image.Image, size int) image.Image {
	if src == nil || size <= 0 {
		return nil
	}

	bounds := src.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()

	if srcW <= 0 || srcH <= 0 {
		return nil
	}

	// Do not upscale images that already fit.
	if srcW <= size && srcH <= size {
		return src
	}

	scale := math.Min(
		float64(size)/float64(srcW),
		float64(size)/float64(srcH),
	)

	dstW := max(1, int(math.Round(float64(srcW)*scale)))
	dstH := max(1, int(math.Round(float64(srcH)*scale)))

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))

	xdraw.CatmullRom.Scale(
		dst,
		dst.Bounds(),
		src,
		bounds,
		xdraw.Src,
		nil,
	)

	return dst
}

func writeAtomic(dir string, finalPath string, write func(*os.File) error) error {
	tmp, err := os.CreateTemp(dir, ".thumbnail-*")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrThumbnailStorage, err)
	}
	tmpName := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		_ = os.Remove(tmpName)
	}()

	if err := write(tmp); err != nil {
		return err
	}
	if !closed {
		if err := tmp.Close(); err != nil {
			return fmt.Errorf("%w: %v", ErrThumbnailStorage, err)
		}
		closed = true
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		if fileExists(finalPath) {
			return nil
		}
		return fmt.Errorf("%w: %v", ErrThumbnailStorage, err)
	}
	return nil
}

type thumbnailKind string

const (
	thumbnailKindUnknown thumbnailKind = "unknown"
	thumbnailKindGeneric thumbnailKind = "generic"
	thumbnailKindImage   thumbnailKind = "image"
	thumbnailKindVideo   thumbnailKind = "video"
)

func thumbnailFileKind(relativeURI string, sourceName string) (thumbnailKind, string) {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(relativeURI)), ".")
	if ext == "" {
		ext = strings.TrimPrefix(strings.ToLower(filepath.Ext(sourceName)), ".")
	}
	if ext == "" {
		return thumbnailKindUnknown, "FILE"
	}
	if slices.Contains([]string{"jpg", "jpeg", "png"}, ext) {
		return thumbnailKindImage, strings.ToUpper(ext)
	}
	if slices.Contains([]string{"mp4", "mov", "m4v", "mkv", "webm", "avi"}, ext) {
		return thumbnailKindVideo, strings.ToUpper(ext)
	}
	return thumbnailKindGeneric, genericLabel(ext)
}

func thumbnailSourceName(source ThumbnailSource) string {
	if source == nil {
		return ""
	}
	return source.Name()
}

func genericLabel(ext string) string {
	ext = strings.TrimSpace(strings.TrimPrefix(ext, "."))
	if ext == "" {
		return "FILE"
	}
	ext = strings.ToUpper(ext)
	if len(ext) > 8 {
		ext = ext[:8]
	}
	return ext
}

func thumbnailFilename(req ThumbnailRequest, extension string) string {
	return fmt.Sprintf("%d_%d_%d%s", req.FileID, req.FileVersion, req.Size, extension)
}

func unknownThumbnailFilename(size int) string {
	return fmt.Sprintf("unknown_%d.svg", size)
}

func genericThumbnailSVG(size int, label string) []byte {
	if size <= 0 {
		size = 256
	}
	label = genericLabel(label)
	escapedLabel := html.EscapeString(label)
	fontSize := max(14, size/5)
	radius := max(4, size/18)
	var buf bytes.Buffer
	fmt.Fprintf(&buf, `<?xml version="1.0" encoding="UTF-8"?>`+"\n")
	fmt.Fprintf(&buf, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="%s file thumbnail">`, size, size, size, size, escapedLabel)
	fmt.Fprintf(&buf, `<rect width="%d" height="%d" rx="%d" fill="#f8fafc"/>`, size, size, radius)
	fmt.Fprintf(&buf, `<rect x="%d" y="%d" width="%d" height="%d" rx="%d" fill="#ffffff" stroke="#d9e0ea" stroke-width="%d"/>`, size/6, size/8, size*2/3, size*3/4, radius, max(1, size/128))
	fmt.Fprintf(&buf, `<text x="50%%" y="54%%" text-anchor="middle" dominant-baseline="middle" font-family="Arial, sans-serif" font-size="%d" font-weight="700" fill="#344054">%s</text>`, fontSize, escapedLabel)
	fmt.Fprintf(&buf, `</svg>`)
	return buf.Bytes()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func ParseThumbnailSize(value string, defaultSize int, allowed []int) (int, error) {
	size := defaultSize
	if strings.TrimSpace(value) != "" {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return 0, ErrInvalidThumbnailSize
		}
		size = parsed
	}
	if size <= 0 || !slices.Contains(allowed, size) {
		return 0, ErrInvalidThumbnailSize
	}
	return size, nil
}

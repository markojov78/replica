package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/disintegration/imaging"
	"github.com/gen2brain/jpegn"

	"replica/internal/config"
)

const ProgressiveJPEGThresholdBytes int64 = 512_000
const progressiveJPEGQuality = 85

var errPreviewOriginal = errors.New("serve original preview")

// ProgressiveJPEGRequest opens the source lazily, after checking the local cache.
// This works with filesystem and remote storage without coupling encoding to either.
type ProgressiveJPEGRequest struct {
	FileID, FileVersion uint
	RelativeURI         string
	Size                int64
	Open                func(context.Context) (io.ReadCloser, error)
}

// ProgressiveJPEGResult holds a generated file lease, or original bytes already
// read while checking eligibility. A zero result means the caller streams the source.
type ProgressiveJPEGResult struct {
	Path     string
	Original []byte
	release  func()
}

func (r ProgressiveJPEGResult) Release() {
	if r.release != nil {
		r.release()
	}
}

type ProgressiveJPEGService struct {
	enabled     bool
	cache       *fileCache
	validateErr error
}

var progressiveJPEGCaches = struct {
	sync.Mutex
	byDirectory map[string]*fileCache
}{byDirectory: make(map[string]*fileCache)}

var progressiveJPEGFilename = regexp.MustCompile(`^[1-9][0-9]*_[1-9][0-9]*\.jpg$`)

func NewProgressiveJPEGService(cfg config.Config) *ProgressiveJPEGService {
	s := &ProgressiveJPEGService{enabled: cfg.Sharing.ProgressiveLoading}
	if s.enabled {
		s.validateErr = s.initializeCache(cfg.Sharing)
	}
	return s
}

func (s *ProgressiveJPEGService) initializeCache(cfg config.SharingConfig) error {
	if cfg.ImageStorageLimitMB <= 0 || uint64(cfg.ImageStorageLimitMB) > uint64(math.MaxInt64/1_000_000) {
		return errors.New("sharing.image_cache_storage_limit_mb must be positive and fit in an int64 byte count")
	}
	dir := strings.TrimSpace(cfg.ImageStorage)
	if dir == "" {
		return errors.New("sharing.image_cache_storage is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dir, err := filepath.Abs(dir)
	if err == nil {
		dir, err = filepath.EvalSymlinks(dir)
	}
	if err != nil {
		return err
	}
	limit := int64(cfg.ImageStorageLimitMB) * 1_000_000
	progressiveJPEGCaches.Lock()
	defer progressiveJPEGCaches.Unlock()
	cache := progressiveJPEGCaches.byDirectory[dir]
	if cache == nil {
		cache, err = newFileCache(dir, limit, progressiveJPEGFilename.MatchString)
		if err != nil {
			return err
		}
		progressiveJPEGCaches.byDirectory[dir] = cache
	} else {
		cache.setLimit(limit)
	}
	s.cache = cache
	return nil
}

func (s *ProgressiveJPEGService) GetOrCreate(ctx context.Context, req ProgressiveJPEGRequest) (ProgressiveJPEGResult, error) {
	decoder := progressiveImageDecoders[strings.ToLower(filepath.Ext(req.RelativeURI))]
	if !s.enabled || req.Size <= ProgressiveJPEGThresholdBytes || decoder == nil {
		return ProgressiveJPEGResult{}, nil
	}
	if req.FileID == 0 || req.FileVersion == 0 || req.Open == nil {
		return ProgressiveJPEGResult{}, errors.New("invalid progressive JPEG request")
	}
	if err := ctx.Err(); err != nil {
		return ProgressiveJPEGResult{}, err
	}
	if s.validateErr != nil {
		return ProgressiveJPEGResult{}, s.validateErr
	}
	var original []byte
	name := fmt.Sprintf("%d_%d.jpg", req.FileID, req.FileVersion)
	lease, err := s.cache.getOrCreate(name, func(path string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		source, err := req.Open(ctx)
		if err != nil {
			return err
		}
		defer source.Close()
		data, err := io.ReadAll(source)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if int64(len(data)) <= ProgressiveJPEGThresholdBytes {
			original = data
			return errPreviewOriginal
		}
		img, err := decoder(data)
		if err != nil {
			return err
		}
		if img == nil {
			original = data
			return errPreviewOriginal
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		err = jpegn.Encode(file, img, &jpegn.EncodeOptions{Quality: progressiveJPEGQuality, Progressive: true})
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		return ctx.Err()
	})
	if errors.Is(err, errPreviewOriginal) {
		return ProgressiveJPEGResult{Original: original}, nil
	}
	if err != nil {
		return ProgressiveJPEGResult{}, err
	}
	return ProgressiveJPEGResult{Path: lease.path, release: lease.release}, nil
}

// Each adapter returns an opaque still image, nil for an unchanged original, or
// a decoding error. Future formats can add an adapter without changing the cache,
// JPEG encoder or HTTP handler. WebP and non-image formats are deliberately absent.
var progressiveImageDecoders = map[string]func([]byte) (image.Image, error){
	".jpg":  decodeProgressiveJPEGSource,
	".jpeg": decodeProgressiveJPEGSource,
	".png":  decodeProgressivePNGSource,
	".gif":  decodeProgressiveGIFSource,
}

func decodeProgressiveJPEGSource(data []byte) (image.Image, error) {
	if _, err := jpeg.DecodeConfig(bytes.NewReader(data)); err != nil {
		return nil, err
	}
	return imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
}

func decodeProgressivePNGSource(data []byte) (image.Image, error) {
	// Preserve animated PNGs too: the still PNG decoder only exposes one frame.
	if len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		for offset := 8; offset+12 <= len(data); {
			size := uint64(binary.BigEndian.Uint32(data[offset:]))
			if size+12 > uint64(len(data)-offset) {
				break
			}
			if string(data[offset+4:offset+8]) == "acTL" {
				return nil, nil
			}
			if string(data[offset+4:offset+8]) == "IEND" {
				break
			}
			offset += int(size) + 12
		}
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if opaque, ok := img.(interface{ Opaque() bool }); ok && !opaque.Opaque() {
		return nil, nil
	}
	return img, nil
}

func decodeProgressiveGIFSource(data []byte) (image.Image, error) {
	animation, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if len(animation.Image) != 1 {
		return nil, nil
	}
	img, err := compositeGIFFrame(animation)
	if err != nil {
		return nil, err
	}
	if !img.(*image.RGBA).Opaque() {
		return nil, nil
	}
	return img, nil
}

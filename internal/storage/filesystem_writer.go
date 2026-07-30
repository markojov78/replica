package storage

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/zeebo/blake3"
)

var ErrFileIntegrityMismatch = errors.New("file integrity mismatch")

type FilesystemWriter struct {
	followSymlinks bool
}

func NewFilesystemWriter(followSymlinks ...bool) *FilesystemWriter {
	return &FilesystemWriter{followSymlinks: len(followSymlinks) > 0 && followSymlinks[0]}
}

func (w *FilesystemWriter) Save(ctx context.Context, replicaURI string, relativeURI string, content io.Reader, size int64) error {
	return w.save(ctx, replicaURI, relativeURI, content, size, "", false)
}

func (w *FilesystemWriter) SaveVerified(ctx context.Context, replicaURI string, relativeURI string, content io.Reader, expectedSize int64, expectedHash string) error {
	return w.save(ctx, replicaURI, relativeURI, content, expectedSize, expectedHash, true)
}

func (w *FilesystemWriter) save(ctx context.Context, replicaURI string, relativeURI string, content io.Reader, expectedSize int64, expectedHash string, verify bool) error {
	targetPath, err := resolveFilesystemWritePath(replicaURI, relativeURI)
	if err != nil {
		return err
	}
	if w.followSymlinks {
		info, statErr := os.Lstat(targetPath)
		if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			targetPath, err = filepath.EvalSymlinks(targetPath)
			if err != nil {
				return err
			}
			targetInfo, err := os.Stat(targetPath)
			if err != nil {
				return err
			}
			if targetInfo.IsDir() {
				return fmt.Errorf("symlink target is a directory: %s", targetPath)
			}
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}

	targetDir := filepath.Dir(targetPath)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}

	tempFile, err := os.CreateTemp(targetDir, temporaryWritePattern())
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()

	var hasher *blake3.Hasher
	destination := io.Writer(tempFile)
	if verify {
		hasher = blake3.New()
		destination = io.MultiWriter(tempFile, hasher)
	}
	written, err := copyWithContext(ctx, destination, content)
	if err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}

	if verify {
		actualHash := hex.EncodeToString(hasher.Sum(nil))
		if written != expectedSize || actualHash != expectedHash {
			return fmt.Errorf(
				"%w: expected size=%d hash=%s, got size=%d hash=%s",
				ErrFileIntegrityMismatch,
				expectedSize,
				expectedHash,
				written,
				actualHash,
			)
		}
	}

	if err := os.Rename(tempPath, targetPath); err != nil {
		return err
	}
	removeTemp = false
	return nil
}

func (w *FilesystemWriter) Delete(_ context.Context, replicaURI string, relativeURI string) error {
	targetPath, err := resolveFilesystemWritePath(replicaURI, relativeURI)
	if err != nil {
		return err
	}

	if err := os.Remove(targetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func resolveFilesystemWritePath(replicaURI string, relativeURI string) (string, error) {
	cleanRelative, err := cleanWriteRelativeURI(relativeURI)
	if err != nil {
		return "", err
	}

	rootPath, err := localFilesystemPath(replicaURI)
	if err != nil {
		return "", err
	}

	root := filepath.Clean(rootPath)
	if info, err := os.Stat(root); err == nil && !info.IsDir() {
		targetRelative := normalizeRelativeURI(filepath.Base(root))
		if cleanRelative != targetRelative {
			return "", fmt.Errorf("relative uri %q does not match single-file replica %q", relativeURI, targetRelative)
		}
		return root, nil
	}

	fullPath := filepath.Join(root, filepath.FromSlash(cleanRelative))
	rel, err := filepath.Rel(root, fullPath)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("relative uri %q escapes replica root", relativeURI)
	}
	return fullPath, nil
}

func cleanWriteRelativeURI(relativeURI string) (string, error) {
	if strings.TrimSpace(relativeURI) == "" {
		return "", errors.New("relative uri is required")
	}
	if path.IsAbs(relativeURI) || hasParentPathSegment(relativeURI) {
		return "", fmt.Errorf("invalid relative uri %q", relativeURI)
	}

	clean := strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(relativeURI)), "/")
	if clean == "." || clean == "" {
		return "", fmt.Errorf("invalid relative uri %q", relativeURI)
	}
	return clean, nil
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, 32*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}

		n, readErr := src.Read(buffer)
		if n > 0 {
			writeN, writeErr := dst.Write(buffer[:n])
			written += int64(writeN)
			if writeErr != nil {
				return written, writeErr
			}
			if writeN != n {
				return written, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

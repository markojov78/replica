package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type FilesystemWriter struct{}

func NewFilesystemWriter() *FilesystemWriter {
	return &FilesystemWriter{}
}

func (w *FilesystemWriter) Save(ctx context.Context, replicaURI string, relativeURI string, content io.Reader) error {
	targetPath, err := resolveFilesystemWritePath(replicaURI, relativeURI)
	if err != nil {
		return err
	}

	targetDir := filepath.Dir(targetPath)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}

	tempFile, err := os.CreateTemp(targetDir, ".dropoutbox-write-*")
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

	if _, err := copyWithContext(ctx, tempFile, content); err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
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

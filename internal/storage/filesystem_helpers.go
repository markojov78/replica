package storage

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zeebo/blake3"
)

type filesystemRoot struct {
	scanPath    string
	relativeDir string
	watchPath   string
	targetPath  string
	singleFile  bool
}

func normalizeRelativeURI(path string) string {
	return strings.TrimPrefix(filepath.ToSlash(path), "./")
}

func resolveFilesystemRoot(rootURI string) (filesystemRoot, error) {
	cleanRoot := filepath.Clean(rootURI)
	info, err := os.Stat(cleanRoot)
	if err != nil {
		return filesystemRoot{}, err
	}

	if info.IsDir() {
		return filesystemRoot{
			scanPath:    cleanRoot,
			relativeDir: cleanRoot,
			watchPath:   cleanRoot,
			targetPath:  cleanRoot,
		}, nil
	}

	parentDir := filepath.Dir(cleanRoot)
	return filesystemRoot{
		scanPath:    cleanRoot,
		relativeDir: parentDir,
		watchPath:   parentDir,
		targetPath:  cleanRoot,
		singleFile:  true,
	}, nil
}

func (r filesystemRoot) includesPath(path string) bool {
	if !r.singleFile {
		return true
	}
	return filepath.Clean(path) == r.targetPath
}

func relativeURI(rootPath, fullPath string) (string, error) {
	relPath, err := filepath.Rel(rootPath, fullPath)
	if err != nil {
		return "", err
	}
	if relPath == "." {
		return "", nil
	}
	return normalizeRelativeURI(relPath), nil
}

func fileStateFromPath(ctx context.Context, rootPath, fullPath string) (*FileState, error) {
	info, err := os.Lstat(fullPath)
	if err != nil {
		return nil, err
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil
	}

	rel, err := relativeURI(rootPath, fullPath)
	if err != nil {
		return nil, err
	}

	hash, err := hashFileBLAKE3(ctx, fullPath)
	if err != nil {
		return nil, err
	}

	created := fileCreatedAt(info)
	return &FileState{
		RelativeURI:   rel,
		Size:          info.Size(),
		Hash:          hash,
		HashAlgorithm: HashAlgorithmBLAKE3,
		Created:       created,
		Modified:      info.ModTime().UTC(),
	}, nil
}

func hashFileBLAKE3(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	return hashReaderBLAKE3(ctx, file)
}

func hashReaderBLAKE3(ctx context.Context, reader io.Reader) (string, error) {
	hasher := blake3.New()
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		n, err := reader.Read(buffer)
		if n > 0 {
			if _, writeErr := hasher.Write(buffer[:n]); writeErr != nil {
				return "", writeErr
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func fileCreatedAt(info os.FileInfo) time.Time {
	if created, ok := fileBirthTime(info); ok {
		return created.UTC()
	}
	return info.ModTime().UTC()
}

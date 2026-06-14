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

func resolveFilesystemTarget(rootURI, targetRelativeURI string) (filesystemRoot, error) {
	targetRelativeURI, err := cleanWriteRelativeURI(targetRelativeURI)
	if err != nil {
		return filesystemRoot{}, err
	}
	rootPath, err := localFilesystemPath(rootURI)
	if err != nil {
		return filesystemRoot{}, err
	}
	cleanRoot := filepath.Clean(rootPath)
	targetPath := filepath.Join(cleanRoot, filepath.FromSlash(targetRelativeURI))
	rel, err := filepath.Rel(cleanRoot, targetPath)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return filesystemRoot{}, errors.New("target relative uri escapes filesystem root")
	}
	return filesystemRoot{
		scanPath:    targetPath,
		relativeDir: cleanRoot,
		watchPath:   filepath.Dir(targetPath),
		targetPath:  targetPath,
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

func fileStateFromPath(ctx context.Context, rootPath, fullPath string, oldStates map[string]FileState) (*FileState, error) {
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

	created := fileCreatedAt(info)
	modified := info.ModTime().UTC()
	if oldState, ok := oldStateWithMatchingMetadata(oldStates, rel, info.Size(), modified); ok {
		return &FileState{
			RelativeURI: rel,
			Size:        info.Size(),
			Hash:        oldState.Hash,
			Created:     created,
			Modified:    modified,
		}, nil
	}

	hash, err := hashFileBLAKE3(ctx, fullPath)
	if err != nil {
		return nil, err
	}

	return &FileState{
		RelativeURI: rel,
		Size:        info.Size(),
		Hash:        hash,
		Created:     created,
		Modified:    modified,
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

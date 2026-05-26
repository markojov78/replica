package storage

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
)

type FilesystemScanner struct{}

func NewFilesystemScanner() *FilesystemScanner {
	return &FilesystemScanner{}
}

func (s *FilesystemScanner) Scan(ctx context.Context, rootURI string) ([]FileState, error) {
	root, err := resolveFilesystemRoot(rootURI)
	if err != nil {
		return nil, err
	}

	states := make([]FileState, 0)

	if root.singleFile {
		state, err := fileStateFromPath(ctx, root.relativeDir, root.scanPath)
		if err != nil {
			return nil, err
		}
		if state != nil {
			states = append(states, *state)
		}
		sortFileStates(states)
		return states, nil
	}

	err = filepath.WalkDir(root.scanPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}

		state, err := fileStateFromPath(ctx, root.relativeDir, path)
		if err != nil {
			return err
		}
		if state == nil {
			return nil
		}

		states = append(states, *state)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sortFileStates(states)
	return states, nil
}

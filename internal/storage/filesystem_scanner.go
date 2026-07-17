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

func (s *FilesystemScanner) Scan(ctx context.Context, rootURI string, oldStates map[string]FileState, targetRelativeURI ...string) ([]FileState, error) {
	if len(targetRelativeURI) > 1 {
		targets, err := cleanRelativeURISet(targetRelativeURI)
		if err != nil {
			return nil, err
		}

		states := make([]FileState, 0, len(targets))
		for target := range targets {
			root, err := resolveFilesystemTarget(rootURI, target)
			if err != nil {
				return nil, err
			}
			if isTemporaryWritePath(root.scanPath) {
				continue
			}
			state, err := fileStateFromPath(ctx, root.relativeDir, root.scanPath, false, oldStates)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if state != nil {
				states = append(states, *state)
			}
		}
		sortFileStates(states)
		return states, nil
	}

	var root filesystemRoot
	var err error
	if len(targetRelativeURI) > 0 && targetRelativeURI[0] != "" {
		root, err = resolveFilesystemTarget(rootURI, targetRelativeURI[0])
	} else {
		root, err = resolveFilesystemRoot(rootURI)
	}
	if err != nil {
		return nil, err
	}

	states := make([]FileState, 0)

	if root.singleFile {
		if isTemporaryWritePath(root.scanPath) {
			return states, nil
		}
		state, err := fileStateFromPath(ctx, root.relativeDir, root.scanPath, false, oldStates)
		if os.IsNotExist(err) {
			return states, nil
		}
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
		if isTemporaryWritePath(path) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}

		state, err := fileStateFromPath(ctx, root.relativeDir, path, false, oldStates)
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

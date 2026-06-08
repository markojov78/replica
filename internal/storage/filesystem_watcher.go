package storage

import (
	"context"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

type FilesystemWatcher struct {
	newWatcher func() (*fsnotify.Watcher, error)
}

func NewFilesystemWatcher() *FilesystemWatcher {
	return &FilesystemWatcher{
		newWatcher: fsnotify.NewWatcher,
	}
}

func (w *FilesystemWatcher) Watch(ctx context.Context, rootURI string) (<-chan FileChange, <-chan error, error) {
	root, err := resolveFilesystemRoot(rootURI)
	if err != nil {
		return nil, nil, err
	}

	fsw, err := w.newWatcher()
	if err != nil {
		return nil, nil, err
	}
	if root.singleFile {
		if err := fsw.Add(root.watchPath); err != nil {
			_ = fsw.Close()
			return nil, nil, err
		}
	} else if err := addRecursiveWatches(fsw, root.watchPath); err != nil {
		_ = fsw.Close()
		return nil, nil, err
	}

	changeCh := make(chan FileChange)
	errCh := make(chan error, 1)

	go w.run(ctx, fsw, root, changeCh, errCh)
	return changeCh, errCh, nil
}

func (w *FilesystemWatcher) run(ctx context.Context, fsw *fsnotify.Watcher, root filesystemRoot, changeCh chan<- FileChange, errCh chan<- error) {
	defer close(changeCh)
	defer close(errCh)
	defer fsw.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-fsw.Errors:
			if !ok {
				return
			}
			sendError(ctx, errCh, err)
			sendChange(ctx, changeCh, FileChange{ChangeType: FileChangeTypeRescanRequired})
		case event, ok := <-fsw.Events:
			if !ok {
				return
			}
			changes, err := filesystemChangesForEvent(ctx, fsw, root, event)
			if err != nil {
				sendError(ctx, errCh, err)
				sendChange(ctx, changeCh, FileChange{ChangeType: FileChangeTypeRescanRequired})
				continue
			}
			for _, change := range changes {
				sendChange(ctx, changeCh, change)
			}
		}
	}
}

func addRecursiveWatches(watcher *fsnotify.Watcher, rootPath string) error {
	return filepath.WalkDir(rootPath, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		return watcher.Add(path)
	})
}

func filesystemChangesForEvent(ctx context.Context, watcher *fsnotify.Watcher, root filesystemRoot, event fsnotify.Event) ([]FileChange, error) {
	if isTemporaryWritePath(event.Name) {
		return nil, nil
	}
	if !root.includesPath(event.Name) {
		if root.singleFile {
			return nil, nil
		}
	}

	if event.Has(fsnotify.Create) && !root.singleFile {
		info, err := os.Lstat(event.Name)
		if err == nil && info.IsDir() {
			if err := addRecursiveWatches(watcher, event.Name); err != nil {
				return nil, err
			}
			return nil, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}

	if event.Has(fsnotify.Remove) {
		_ = watcher.Remove(event.Name)
	}

	state, err := fileStateFromPath(ctx, root.relativeDir, event.Name, nil)
	if err == nil && state != nil {
		changeType := FileChangeTypeModified
		if event.Has(fsnotify.Create) {
			changeType = FileChangeTypeCreated
		}
		stateCopy := *state
		return []FileChange{{
			RelativeURI: state.RelativeURI,
			ChangeType:  changeType,
			State:       &stateCopy,
		}}, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	rel, relErr := relativeURI(root.relativeDir, event.Name)
	if relErr != nil {
		return nil, relErr
	}

	if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
		return []FileChange{{
			RelativeURI: rel,
			ChangeType:  FileChangeTypeDeleted,
		}}, nil
	}

	return []FileChange{{
		RelativeURI: rel,
		ChangeType:  FileChangeTypeRescanRequired,
	}}, nil
}

func sendChange(ctx context.Context, changeCh chan<- FileChange, change FileChange) {
	select {
	case <-ctx.Done():
	case changeCh <- change:
	}
}

func sendError(ctx context.Context, errCh chan<- error, err error) {
	select {
	case <-ctx.Done():
	case errCh <- err:
	default:
	}
}

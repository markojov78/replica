package storage

import (
	"context"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

type FilesystemWatcher struct {
	newWatcher     func() (*fsnotify.Watcher, error)
	followSymlinks bool
}

func NewFilesystemWatcher(followSymlinks ...bool) *FilesystemWatcher {
	return &FilesystemWatcher{
		newWatcher:     fsnotify.NewWatcher,
		followSymlinks: len(followSymlinks) > 0 && followSymlinks[0],
	}
}

func (w *FilesystemWatcher) Watch(ctx context.Context, rootURI string, targetRelativeURIs []string) (<-chan FileChange, <-chan error, error) {
	root, err := resolveFilesystemWatcherRoot(rootURI, targetRelativeURIs)
	if err != nil {
		return nil, nil, err
	}

	fsw, err := w.newWatcher()
	if err != nil {
		return nil, nil, err
	}
	if err := addRecursiveWatches(fsw, root.watchPath); err != nil {
		_ = fsw.Close()
		return nil, nil, err
	}
	if w.followSymlinks {
		root.symlinkTargets = make(map[string][]string)
		root.symlinkTargetByLink = make(map[string]string)
		if err := addFileSymlinkWatches(fsw, &root); err != nil {
			_ = fsw.Close()
			return nil, nil, err
		}
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
			changes, err := filesystemChangesForEvent(ctx, fsw, root, event, w.followSymlinks)
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

func addFileSymlinkWatches(watcher *fsnotify.Watcher, root *filesystemRoot) error {
	return filepath.WalkDir(root.scanPath, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		return registerFileSymlinkWatch(watcher, root, path)
	})
}

func registerFileSymlinkWatch(watcher *fsnotify.Watcher, root *filesystemRoot, linkPath string) error {
	targetPath, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return nil
	}
	linkPath = filepath.Clean(linkPath)
	targetPath = filepath.Clean(targetPath)
	if previous, ok := root.symlinkTargetByLink[linkPath]; ok && previous == targetPath {
		return nil
	}
	if err := watcher.Add(filepath.Dir(targetPath)); err != nil {
		return err
	}
	root.symlinkTargetByLink[linkPath] = targetPath
	root.symlinkTargets[targetPath] = append(root.symlinkTargets[targetPath], linkPath)
	return nil
}

func unregisterFileSymlink(root filesystemRoot, linkPath string) {
	linkPath = filepath.Clean(linkPath)
	targetPath, ok := root.symlinkTargetByLink[linkPath]
	if !ok {
		return
	}
	delete(root.symlinkTargetByLink, linkPath)
	links := root.symlinkTargets[targetPath]
	for i, candidate := range links {
		if candidate == linkPath {
			links = append(links[:i], links[i+1:]...)
			break
		}
	}
	if len(links) == 0 {
		delete(root.symlinkTargets, targetPath)
	} else {
		root.symlinkTargets[targetPath] = links
	}
}

func filesystemChangesForEvent(ctx context.Context, watcher *fsnotify.Watcher, root filesystemRoot, event fsnotify.Event, followSymlinks ...bool) ([]FileChange, error) {
	if isTemporaryWritePath(event.Name) {
		return nil, nil
	}
	if event.Has(fsnotify.Create) {
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
		if err == nil && followSymlinkEnabled(followSymlinks) && info.Mode()&os.ModeSymlink != 0 {
			if err := registerFileSymlinkWatch(watcher, &root, event.Name); err != nil {
				return nil, err
			}
		}
	}

	if links := root.symlinkTargets[filepath.Clean(event.Name)]; len(links) > 0 {
		changes := make([]FileChange, 0, len(links))
		for _, linkPath := range links {
			state, stateErr := fileStateFromPath(ctx, root.relativeDir, linkPath, true, nil)
			if stateErr == nil && state != nil {
				stateCopy := *state
				changes = append(changes, FileChange{RelativeURI: state.RelativeURI, ChangeType: FileChangeTypeModified, State: &stateCopy})
				continue
			}
			if stateErr != nil && !os.IsNotExist(stateErr) {
				return nil, stateErr
			}
			rel, err := relativeURI(root.relativeDir, linkPath)
			if err != nil {
				return nil, err
			}
			changes = append(changes, FileChange{RelativeURI: rel, ChangeType: FileChangeTypeDeleted})
		}
		return changes, nil
	}

	if !root.includesPath(event.Name) {
		return nil, nil
	}

	if event.Has(fsnotify.Remove) {
		_ = watcher.Remove(event.Name)
		unregisterFileSymlink(root, event.Name)
	}

	followSymlink := followSymlinkEnabled(followSymlinks)
	state, err := fileStateFromPath(ctx, root.relativeDir, event.Name, followSymlink, nil)
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

func followSymlinkEnabled(values []bool) bool {
	return len(values) > 0 && values[0]
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

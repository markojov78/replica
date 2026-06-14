package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestFilesystemChangesForEventSingleFileRootIgnoresOtherFiles(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "target.txt")
	otherPath := filepath.Join(rootDir, "other.txt")
	if err := os.WriteFile(targetPath, []byte("target"), 0o644); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	if err := os.WriteFile(otherPath, []byte("other"), 0o644); err != nil {
		t.Fatalf("WriteFile(other) error = %v", err)
	}

	root, err := resolveFilesystemRoot(targetPath)
	if err != nil {
		t.Fatalf("resolveFilesystemRoot() error = %v", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("NewWatcher() error = %v", err)
	}
	defer watcher.Close()

	changes, err := filesystemChangesForEvent(context.Background(), watcher, root, fsnotify.Event{
		Name: otherPath,
		Op:   fsnotify.Write,
	})
	if err != nil {
		t.Fatalf("filesystemChangesForEvent() error = %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("len(changes) = %d, want 0", len(changes))
	}
}

func TestFilesystemChangesForEventIgnoresTemporaryWritePaths(t *testing.T) {
	rootDir := t.TempDir()
	tempPath := filepath.Join(rootDir, "nested", TemporaryWritePrefix+"123")
	if err := os.MkdirAll(filepath.Dir(tempPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(tempPath, []byte("temporary"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	root, err := resolveFilesystemRoot(rootDir)
	if err != nil {
		t.Fatalf("resolveFilesystemRoot() error = %v", err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("NewWatcher() error = %v", err)
	}
	defer watcher.Close()

	for _, op := range []fsnotify.Op{fsnotify.Create, fsnotify.Write, fsnotify.Remove, fsnotify.Rename} {
		changes, err := filesystemChangesForEvent(context.Background(), watcher, root, fsnotify.Event{Name: tempPath, Op: op})
		if err != nil {
			t.Fatalf("filesystemChangesForEvent(%s) error = %v", op, err)
		}
		if len(changes) != 0 {
			t.Fatalf("filesystemChangesForEvent(%s) changes = %+v, want empty", op, changes)
		}
	}
}

func TestFilesystemWatcherSingleFileRootEmitsTargetChanges(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "target.txt")
	if err := os.WriteFile(targetPath, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	watcher := NewFilesystemWatcher()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	changeCh, errCh, err := watcher.Watch(ctx, targetPath)
	if err != nil {
		t.Fatalf("Watch() error = %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(targetPath, []byte("after"), 0o644); err != nil {
		t.Fatalf("WriteFile(update) error = %v", err)
	}

	for {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("watcher error = %v", err)
			}
		case change := <-changeCh:
			if change.RelativeURI != "target.txt" {
				continue
			}
			if change.ChangeType != FileChangeTypeModified && change.ChangeType != FileChangeTypeCreated {
				t.Fatalf("change.ChangeType = %q, want modified or created", change.ChangeType)
			}
			if change.State == nil {
				t.Fatal("change.State is nil")
			}
			if change.State.RelativeURI != "target.txt" {
				t.Fatalf("change.State.RelativeURI = %q, want %q", change.State.RelativeURI, "target.txt")
			}
			return
		case <-ctx.Done():
			t.Fatal("timed out waiting for single-file watcher event")
		}
	}
}

func TestFilesystemWatcherExplicitTargetIgnoresOtherFiles(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "target.txt")
	otherPath := filepath.Join(rootDir, "other.txt")

	watcher := NewFilesystemWatcher()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	changeCh, errCh, err := watcher.Watch(ctx, rootDir, "target.txt")
	if err != nil {
		t.Fatalf("Watch() error = %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(otherPath, []byte("other"), 0o644); err != nil {
		t.Fatalf("WriteFile(other) error = %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("target"), 0o644); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}

	for {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("watcher error = %v", err)
			}
		case change := <-changeCh:
			if change.RelativeURI == "other.txt" {
				t.Fatalf("watcher reported unrelated file: %+v", change)
			}
			if change.RelativeURI == "target.txt" {
				return
			}
		case <-ctx.Done():
			t.Fatal("timed out waiting for explicit target watcher event")
		}
	}
}

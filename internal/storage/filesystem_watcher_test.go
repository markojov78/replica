package storage

import (
	"context"
	"fmt"
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

func TestFilesystemWatcherRejectsSingleFileRoot(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "target.txt")
	if err := os.WriteFile(targetPath, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	watcher := NewFilesystemWatcher()
	if _, _, err := watcher.Watch(context.Background(), targetPath, nil); err == nil {
		t.Fatal("Watch() error = nil, want single-file root error")
	}
}

func TestFilesystemWatcherExplicitTargetsIgnoreOtherFiles(t *testing.T) {
	rootDir := t.TempDir()
	firstTargetPath := filepath.Join(rootDir, "first.txt")
	secondTargetPath := filepath.Join(rootDir, "nested", "second.txt")
	otherPath := filepath.Join(rootDir, "other.txt")
	if err := os.MkdirAll(filepath.Dir(secondTargetPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	watcher := NewFilesystemWatcher()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	changeCh, errCh, err := watcher.Watch(ctx, rootDir, []string{"first.txt", "nested/second.txt"})
	if err != nil {
		t.Fatalf("Watch() error = %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(otherPath, []byte("other"), 0o644); err != nil {
		t.Fatalf("WriteFile(other) error = %v", err)
	}
	if err := os.WriteFile(firstTargetPath, []byte("first"), 0o644); err != nil {
		t.Fatalf("WriteFile(first target) error = %v", err)
	}
	if err := os.WriteFile(secondTargetPath, []byte("second"), 0o644); err != nil {
		t.Fatalf("WriteFile(second target) error = %v", err)
	}

	seen := make(map[string]bool)
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
			seen[change.RelativeURI] = true
			if seen["first.txt"] && seen["nested/second.txt"] {
				return
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for explicit target watcher events; seen=%v", seen)
		}
	}
}

func TestFilesystemWatcherEmptyTargetsWatchTree(t *testing.T) {
	for _, targets := range [][]string{nil, {}} {
		t.Run(fmt.Sprintf("targets_%v", targets), func(t *testing.T) {
			rootDir := t.TempDir()
			watcher := NewFilesystemWatcher()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			changeCh, errCh, err := watcher.Watch(ctx, rootDir, targets)
			if err != nil {
				t.Fatalf("Watch() error = %v", err)
			}
			time.Sleep(100 * time.Millisecond)

			if err := os.WriteFile(filepath.Join(rootDir, "file.txt"), []byte("content"), 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			for {
				select {
				case err := <-errCh:
					if err != nil {
						t.Fatalf("watcher error = %v", err)
					}
				case change := <-changeCh:
					if change.RelativeURI == "file.txt" {
						return
					}
				case <-ctx.Done():
					t.Fatal("timed out waiting for tree watcher event")
				}
			}
		})
	}
}

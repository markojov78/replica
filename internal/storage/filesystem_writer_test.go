package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetWriterReturnsFilesystemWriter(t *testing.T) {
	writer, err := GetWriter(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("GetWriter() error = %v", err)
	}
	if _, ok := writer.(*FilesystemWriter); !ok {
		t.Fatalf("GetWriter() = %T, want *FilesystemWriter", writer)
	}
}

func TestGetWriterRejectsUnsupportedScheme(t *testing.T) {
	if _, err := GetWriter(context.Background(), "ftp://example/path", nil); err == nil {
		t.Fatal("GetWriter() error = nil, want unsupported scheme error")
	}
}

func TestFilesystemWriterSaveAndDelete(t *testing.T) {
	root := t.TempDir()
	writer := NewFilesystemWriter()

	if err := writer.Save(context.Background(), root, "nested/file.txt", strings.NewReader("content"), int64(len("content"))); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	target := filepath.Join(root, "nested", "file.txt")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "content" {
		t.Fatalf("content = %q, want %q", string(data), "content")
	}

	if err := writer.Delete(context.Background(), root, "nested/file.txt"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("Stat() error = %v, want not exist", err)
	}
	if err := writer.Delete(context.Background(), root, "nested/file.txt"); err != nil {
		t.Fatalf("Delete(missing) error = %v", err)
	}
}

func TestFilesystemWriterRejectsPathTraversal(t *testing.T) {
	writer := NewFilesystemWriter()
	if err := writer.Save(context.Background(), t.TempDir(), "../escape.txt", strings.NewReader("content"), int64(len("content"))); err == nil {
		t.Fatal("Save() error = nil, want path traversal rejection")
	}
}

func TestTemporaryWritePatternUsesCentralizedPrefix(t *testing.T) {
	if got := temporaryWritePattern(); got != TemporaryWritePrefix+"*" {
		t.Fatalf("temporaryWritePattern() = %q, want %q", got, TemporaryWritePrefix+"*")
	}
}

func TestFilesystemWriterUpdatesSymlinkTargetAndDeletesLink(t *testing.T) {
	root := t.TempDir()
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "target.txt")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	link := filepath.Join(root, "linked.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	writer := NewFilesystemWriter(true)
	if err := writer.Save(context.Background(), root, "linked.txt", strings.NewReader("new content"), int64(len("new content"))); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("Lstat(link) info=%v error=%v, want symlink preserved", info, err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile(target) error = %v", err)
	}
	if string(content) != "new content" {
		t.Fatalf("target content = %q, want new content", content)
	}

	if err := writer.Delete(context.Background(), root, "linked.txt"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("Lstat(link) error = %v, want link removed", err)
	}
	content, err = os.ReadFile(target)
	if err != nil || string(content) != "new content" {
		t.Fatalf("target after delete content=%q error=%v, want preserved", content, err)
	}
}

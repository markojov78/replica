package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFilesystemScannerScansNestedFilesWithNormalizedPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested", "deep"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "deep", "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile(hello.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "root.bin"), []byte("abc"), 0o644); err != nil {
		t.Fatalf("WriteFile(root.bin) error = %v", err)
	}

	scanner := NewFilesystemScanner()
	states, err := scanner.Scan(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(states) != 2 {
		t.Fatalf("len(states) = %d, want 2", len(states))
	}
	if states[0].RelativeURI != "nested/deep/hello.txt" {
		t.Fatalf("states[0].RelativeURI = %q, want %q", states[0].RelativeURI, "nested/deep/hello.txt")
	}
	if states[1].RelativeURI != "root.bin" {
		t.Fatalf("states[1].RelativeURI = %q, want %q", states[1].RelativeURI, "root.bin")
	}
}

func TestFilesystemScannerCalculatesStableBLAKE3Hashes(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "file.txt")
	if err := os.WriteFile(filePath, []byte("stable content"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	scanner := NewFilesystemScanner()
	first, err := scanner.Scan(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("first Scan() error = %v", err)
	}
	second, err := scanner.Scan(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("second Scan() error = %v", err)
	}

	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("unexpected scan lengths: len(first)=%d len(second)=%d", len(first), len(second))
	}
	if first[0].Hash == "" {
		t.Fatal("first[0].Hash is empty")
	}
	if first[0].Hash != second[0].Hash {
		t.Fatalf("hash mismatch: %q != %q", first[0].Hash, second[0].Hash)
	}
}

func TestFilesystemScannerReusesOldHashWhenMetadataIsUnchanged(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "file.txt")
	content := []byte("stable content")
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	modified := time.Date(2026, 5, 1, 12, 30, 0, 0, time.UTC)
	if err := os.Chtimes(filePath, modified, modified); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	oldStates := map[string]FileState{
		"file.txt": {
			RelativeURI: "file.txt",
			Size:        int64(len(content)),
			Hash:        "known-hash",
			Modified:    modified,
		},
	}

	states, err := NewFilesystemScanner().Scan(context.Background(), root, oldStates)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("len(states) = %d, want 1", len(states))
	}
	if states[0].Hash != "known-hash" {
		t.Fatalf("states[0].Hash = %q, want old hash", states[0].Hash)
	}
}

func TestFilesystemScannerRehashesWhenMetadataChanged(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "file.txt")
	if err := os.WriteFile(filePath, []byte("changed content"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	modified := time.Date(2026, 5, 1, 12, 30, 0, 0, time.UTC)
	if err := os.Chtimes(filePath, modified, modified); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	oldStates := map[string]FileState{
		"file.txt": {
			RelativeURI: "file.txt",
			Size:        1,
			Hash:        "known-hash",
			Modified:    modified,
		},
	}

	states, err := NewFilesystemScanner().Scan(context.Background(), root, oldStates)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	wantHash, err := hashReaderBLAKE3(context.Background(), strings.NewReader("changed content"))
	if err != nil {
		t.Fatalf("hashReaderBLAKE3() error = %v", err)
	}
	if len(states) != 1 || states[0].Hash != wantHash {
		t.Fatalf("states = %+v, want rehashed content hash %q", states, wantHash)
	}
}

func TestFilesystemScannerReturnsSizeAndModifiedTime(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "file.txt")
	content := []byte("1234567890")
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	modified := time.Date(2026, 5, 1, 12, 30, 0, 0, time.UTC)
	if err := os.Chtimes(filePath, modified, modified); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	scanner := NewFilesystemScanner()
	states, err := scanner.Scan(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("len(states) = %d, want 1", len(states))
	}
	if states[0].Size != int64(len(content)) {
		t.Fatalf("states[0].Size = %d, want %d", states[0].Size, len(content))
	}
	if !states[0].Modified.Equal(modified) {
		t.Fatalf("states[0].Modified = %s, want %s", states[0].Modified, modified)
	}
	if states[0].Created.IsZero() {
		t.Fatal("states[0].Created is zero")
	}
}

func TestFilesystemScannerScansSingleFileRoot(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "single.txt")
	if err := os.WriteFile(filePath, []byte("one file"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	scanner := NewFilesystemScanner()
	states, err := scanner.Scan(context.Background(), filePath, nil)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("len(states) = %d, want 1", len(states))
	}
	if states[0].RelativeURI != "single.txt" {
		t.Fatalf("states[0].RelativeURI = %q, want %q", states[0].RelativeURI, "single.txt")
	}
}

func TestFilesystemScannerScansOnlyExplicitTargetRelativeURI(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "target.txt"), []byte("target"), 0o644); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "other.txt"), []byte("other"), 0o644); err != nil {
		t.Fatalf("WriteFile(other) error = %v", err)
	}

	scanner := NewFilesystemScanner()
	states, err := scanner.Scan(context.Background(), root, nil, "target.txt")
	if err != nil {
		t.Fatalf("Scan(target) error = %v", err)
	}
	if len(states) != 1 || states[0].RelativeURI != "target.txt" {
		t.Fatalf("states = %+v, want only target.txt", states)
	}

	states, err = scanner.Scan(context.Background(), root, nil, "missing.txt")
	if err != nil {
		t.Fatalf("Scan(missing target) error = %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("missing target states = %+v, want empty", states)
	}
}

func TestFilesystemScannerScansMultipleExplicitTargetRelativeURIs(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{"first.txt", "nested/second.txt", "other.txt"} {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", relative, err)
		}
		if err := os.WriteFile(path, []byte(relative), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", relative, err)
		}
	}

	states, err := NewFilesystemScanner().Scan(context.Background(), root, nil, "nested/second.txt", "first.txt")
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(states) != 2 || states[0].RelativeURI != "first.txt" || states[1].RelativeURI != "nested/second.txt" {
		t.Fatalf("states = %+v, want first.txt and nested/second.txt", states)
	}
}

func TestFilesystemScannerIgnoresTemporaryWritePaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	for _, relative := range []string{
		TemporaryWritePrefix + "root",
		filepath.Join("nested", TemporaryWritePrefix+"nested"),
		"visible.txt",
	} {
		if err := os.WriteFile(filepath.Join(root, relative), []byte(relative), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", relative, err)
		}
	}

	states, err := NewFilesystemScanner().Scan(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(states) != 1 || states[0].RelativeURI != "visible.txt" {
		t.Fatalf("states = %+v, want only visible.txt", states)
	}
}

func TestFilesystemScannerIgnoresTemporarySingleFileRoot(t *testing.T) {
	root := t.TempDir()
	tempPath := filepath.Join(root, TemporaryWritePrefix+"single")
	if err := os.WriteFile(tempPath, []byte("temporary"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	states, err := NewFilesystemScanner().Scan(context.Background(), tempPath, nil)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("states = %+v, want empty", states)
	}
}

func TestFilesystemScannerFollowsFileSymlinksWhenEnabled(t *testing.T) {
	root := t.TempDir()
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "target.txt")
	if err := os.WriteFile(target, []byte("first"), 0o644); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	link := filepath.Join(root, "linked.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	ignored, err := NewFilesystemScanner().Scan(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Scan(disabled) error = %v", err)
	}
	if len(ignored) != 0 {
		t.Fatalf("disabled states = %+v, want empty", ignored)
	}

	first, err := NewFilesystemScanner(true).Scan(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Scan(enabled) error = %v", err)
	}
	if len(first) != 1 || first[0].RelativeURI != "linked.txt" || first[0].Size != 5 {
		t.Fatalf("first states = %+v, want linked.txt target metadata", first)
	}

	if err := os.WriteFile(target, []byte("updated target"), 0o644); err != nil {
		t.Fatalf("WriteFile(updated target) error = %v", err)
	}
	second, err := NewFilesystemScanner(true).Scan(context.Background(), root, fileStateMap(first))
	if err != nil {
		t.Fatalf("Scan(updated target) error = %v", err)
	}
	if len(second) != 1 || second[0].Size != int64(len("updated target")) || second[0].Hash == first[0].Hash {
		t.Fatalf("second states = %+v, want changed target state", second)
	}
}

func TestFilesystemScannerIgnoresDirectorySymlinksWhenFollowingFiles(t *testing.T) {
	root := t.TempDir()
	targetDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(targetDir, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Symlink(targetDir, filepath.Join(root, "linked-dir")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	states, err := NewFilesystemScanner(true).Scan(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("states = %+v, want directory symlink ignored", states)
	}
}

func TestFilesystemScannerSkipsBrokenFileSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "regular.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "missing.txt"), filepath.Join(root, "broken.txt")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	states, err := NewFilesystemScanner(true).Scan(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(states) != 1 || states[0].RelativeURI != "regular.txt" {
		t.Fatalf("states = %+v, want only regular.txt", states)
	}
}

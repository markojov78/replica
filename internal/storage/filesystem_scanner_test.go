package storage

import (
	"context"
	"os"
	"path/filepath"
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
	states, err := scanner.Scan(context.Background(), root)
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
	first, err := scanner.Scan(context.Background(), root)
	if err != nil {
		t.Fatalf("first Scan() error = %v", err)
	}
	second, err := scanner.Scan(context.Background(), root)
	if err != nil {
		t.Fatalf("second Scan() error = %v", err)
	}

	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("unexpected scan lengths: len(first)=%d len(second)=%d", len(first), len(second))
	}
	if first[0].HashAlgorithm != HashAlgorithmBLAKE3 {
		t.Fatalf("first[0].HashAlgorithm = %q, want %q", first[0].HashAlgorithm, HashAlgorithmBLAKE3)
	}
	if first[0].Hash == "" {
		t.Fatal("first[0].Hash is empty")
	}
	if first[0].Hash != second[0].Hash {
		t.Fatalf("hash mismatch: %q != %q", first[0].Hash, second[0].Hash)
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
	states, err := scanner.Scan(context.Background(), root)
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
	states, err := scanner.Scan(context.Background(), filePath)
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

	states, err := NewFilesystemScanner().Scan(context.Background(), root)
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

	states, err := NewFilesystemScanner().Scan(context.Background(), tempPath)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("states = %+v, want empty", states)
	}
}

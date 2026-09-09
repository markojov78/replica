package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func cacheTestFile(t *testing.T, dir, name string, size int, age int64) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(age, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return path
}

func testFileCache(t *testing.T, dir string, limit int64) *fileCache {
	t.Helper()
	cache, err := newFileCache(dir, limit, func(name string) bool { return strings.HasSuffix(name, ".bin") })
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

func assertCachePath(t *testing.T, path string, exists bool) {
	t.Helper()
	_, err := os.Lstat(path)
	if exists && err != nil || !exists && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Lstat(%s) = %v, expected exists=%t", path, err, exists)
	}
}

func TestFileCacheStartupEvictsByAgeThenFilename(t *testing.T) {
	dir := t.TempDir()
	cacheTestFile(t, dir, "z-old.bin", 3, 1)
	cacheTestFile(t, dir, "a.bin", 3, 2)
	cacheTestFile(t, dir, "b.bin", 3, 2)
	cacheTestFile(t, dir, "new.bin", 3, 3)
	cache := testFileCache(t, dir, 6)
	for _, name := range []string{"z-old.bin", "a.bin"} {
		assertCachePath(t, filepath.Join(dir, name), false)
	}
	for _, name := range []string{"b.bin", "new.bin"} {
		assertCachePath(t, filepath.Join(dir, name), true)
	}
	if cache.bytes != 6 {
		t.Fatalf("accounted bytes = %d, want 6", cache.bytes)
	}
}

func TestFileCacheHitsDoNotRefreshAge(t *testing.T) {
	dir := t.TempDir()
	old := cacheTestFile(t, dir, "old.bin", 3, 1)
	cacheTestFile(t, dir, "new.bin", 3, 2)
	cache := testFileCache(t, dir, 6)
	lease, ok, err := cache.acquire("old.bin")
	if err != nil || !ok {
		t.Fatalf("acquire = %t, %v", ok, err)
	}
	lease.release()
	info, err := os.Stat(old)
	if err != nil || !info.ModTime().Equal(time.Unix(1, 0)) {
		t.Fatalf("hit changed age: %v, %v", info, err)
	}
	generated, err := cache.getOrCreate("generated.bin", func(path string) error {
		return os.WriteFile(path, []byte("123"), 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer generated.release()
	assertCachePath(t, old, false)
	assertCachePath(t, filepath.Join(dir, "new.bin"), true)
}

func TestFileCacheProtectsMultipleReadersBeforeOpenAndRetriesOnRelease(t *testing.T) {
	dir := t.TempDir()
	path := cacheTestFile(t, dir, "active.bin", 4, 1)
	cache := testFileCache(t, dir, 8)
	first, _, err := cache.acquire("active.bin")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := cache.acquire("active.bin")
	if err != nil {
		t.Fatal(err)
	}
	cache.setLimit(1)
	// Eviction runs between acquisition and opening the file.
	f, err := os.Open(first.path)
	if err != nil {
		t.Fatal(err)
	}
	first.release()
	first.release() // Copies and repeated releases must be harmless.
	assertCachePath(t, path, true)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	second.release()
	assertCachePath(t, path, false)
	if cache.bytes != 0 {
		t.Fatalf("bytes after release = %d", cache.bytes)
	}
}

func TestFileCacheConcurrentGenerationAndServing(t *testing.T) {
	cache := testFileCache(t, t.TempDir(), 100)
	started, proceed := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	generate := func(path string) error {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-proceed
		return os.WriteFile(path, []byte("same"), 0o600)
	}
	type outcome struct {
		lease fileCacheLease
		err   error
	}
	results := make(chan outcome, 16)
	for range 16 {
		go func() {
			lease, err := cache.getOrCreate("same.bin", generate)
			results <- outcome{lease, err}
		}()
	}
	<-started
	// A different generation and cleanup must finish while the first is blocked.
	other, err := cache.getOrCreate("other.bin", func(path string) error {
		return os.WriteFile(path, []byte("other"), 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	other.release()
	close(proceed)
	for range 16 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		defer result.lease.release()
		body, err := os.ReadFile(result.lease.path)
		if err != nil || string(body) != "same" {
			t.Fatalf("read = %q, %v", body, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("generation count = %d", calls.Load())
	}
}

func TestFileCacheOversizedEntryUsesTemporaryBacking(t *testing.T) {
	cache := testFileCache(t, t.TempDir(), 3)
	first, err := cache.getOrCreate("large.bin", func(path string) error {
		return os.WriteFile(path, []byte("oversized"), 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCachePath(t, filepath.Join(cache.dir, "large.bin"), false)
	second, ok, err := cache.acquire("large.bin")
	if err != nil || !ok || second.path != first.path {
		t.Fatalf("second reader = %+v, %t, %v", second, ok, err)
	}
	first.release()
	assertCachePath(t, second.path, true)
	second.release()
	assertCachePath(t, second.path, false)
	if cache.bytes != 0 {
		t.Fatalf("accounting after oversized release = %d", cache.bytes)
	}
}

func TestFileCacheDeletionFailureRemainsAccountedAndTriesOtherEntries(t *testing.T) {
	dir := t.TempDir()
	old := cacheTestFile(t, dir, "old.bin", 2, 1)
	middle := cacheTestFile(t, dir, "middle.bin", 2, 2)
	newest := cacheTestFile(t, dir, "new.bin", 2, 3)
	cache := testFileCache(t, dir, 10)
	cache.remove = func(path string) error {
		if path == old {
			return os.ErrPermission
		}
		return os.Remove(path)
	}
	cache.setLimit(3)
	assertCachePath(t, old, true)
	assertCachePath(t, middle, false)
	assertCachePath(t, newest, false)
	if cache.bytes != 2 {
		t.Fatalf("failed deletion accounting = %d, want 2", cache.bytes)
	}
	lease, err := cache.getOrCreate("usable.bin", func(path string) error {
		return os.WriteFile(path, []byte("12"), 0o600)
	})
	if err != nil {
		t.Fatalf("cleanup failure prevented usable result: %v", err)
	}
	assertCachePath(t, lease.path, true)
	cache.remove = os.Remove
	lease.release()
	assertCachePath(t, old, false)
	assertCachePath(t, lease.path, true)
	if cache.bytes != 2 {
		t.Fatalf("accounting after retry = %d", cache.bytes)
	}
}

func TestFileCachePreservesUnrelatedAndNonRegularEntries(t *testing.T) {
	dir := t.TempDir()
	unrelated := cacheTestFile(t, dir, "unrelated.txt", 100, 1)
	tmp := cacheTestFile(t, dir, ".file-cache-active", 100, 1)
	directory := filepath.Join(dir, "directory.bin")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "symlink.bin")
	if err := os.Symlink(unrelated, symlink); err != nil {
		t.Fatal(err)
	}
	cacheTestFile(t, dir, "owned.bin", 5, 1)
	cache := testFileCache(t, dir, 1)
	for _, path := range []string{unrelated, tmp, directory, symlink} {
		assertCachePath(t, path, true)
	}
	for _, name := range []string{"directory.bin", "symlink.bin"} {
		_, err := cache.getOrCreate(name, func(string) error {
			t.Fatal("generation must not run for invalid cache entries")
			return nil
		})
		if err == nil {
			t.Fatalf("expected error for %s", name)
		}
	}
	if cache.bytes != 0 {
		t.Fatalf("unexpected files counted: %d", cache.bytes)
	}
}

func TestFileCacheFailedGenerationCleansTemporaryBacking(t *testing.T) {
	cache := testFileCache(t, t.TempDir(), 3)
	want := fmt.Errorf("generation failed")
	_, err := cache.getOrCreate("failed.bin", func(path string) error {
		if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	files, err := os.ReadDir(cache.dir)
	if err != nil || len(files) != 0 || cache.bytes != 0 {
		t.Fatalf("generation left files/accounting: %v, %d, %v", files, cache.bytes, err)
	}
}

func TestFileCacheOversizedCleanupFailureIsAccountedAndRetried(t *testing.T) {
	cache := testFileCache(t, t.TempDir(), 3)
	lease, err := cache.getOrCreate("large.bin", func(path string) error {
		return os.WriteFile(path, []byte("large"), 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	cache.remove = func(string) error { return os.ErrPermission }
	lease.release()
	assertCachePath(t, lease.path, true)
	if cache.bytes != 5 {
		t.Fatalf("failed temporary deletion accounting = %d", cache.bytes)
	}
	// Raising the budget must not turn temporary backing into a retained entry.
	cache.remove = os.Remove
	cache.setLimit(10)
	assertCachePath(t, lease.path, false)
	if cache.bytes != 0 {
		t.Fatalf("temporary deletion retry accounting = %d", cache.bytes)
	}
}

package service

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// fileCache manages disposable generated files in a directory owned by one server
// process. It is independent of content encoding. All acquisitions, publication,
// accounting and eviction share mu; generation and response IO never hold it.
type fileCache struct {
	dir     string
	limit   int64
	owned   func(string) bool
	mu      sync.Mutex
	entries map[string]*fileCacheEntry
	locks   map[string]*fileCacheLock
	bytes   int64
	remove  func(string) error
	pending bool
}

type fileCacheEntry struct {
	name       string
	path       string
	size       int64
	generated  time.Time
	users      int
	disposable bool // Oversized completed files keep their temporary backing.
}

type fileCacheLock struct {
	mu    sync.Mutex
	users int
}

type fileCacheLease struct {
	path    string
	release func()
}

func newFileCache(dir string, limit int64, owned func(string) bool) (*fileCache, error) {
	c := &fileCache{
		dir: dir, limit: limit, owned: owned,
		entries: make(map[string]*fileCacheEntry), locks: make(map[string]*fileCacheLock),
		remove: os.Remove,
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if !owned(file.Name()) {
			continue
		}
		info, err := file.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			log.Printf("file cache: unexpected non-regular entry %s", filepath.Join(dir, file.Name()))
			continue
		}
		c.recordLocked(file.Name(), filepath.Join(dir, file.Name()), info)
	}
	c.cleanupLocked()
	return c, nil
}

func (c *fileCache) setLimit(limit int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.limit = limit
	c.cleanupLocked()
}

func (c *fileCache) recordLocked(name, path string, info os.FileInfo) *fileCacheEntry {
	e := &fileCacheEntry{name: name, path: path, size: info.Size(), generated: info.ModTime()}
	c.entries[name] = e
	c.bytes += e.size
	return e
}

// acquire protects the pathname before returning it, including the caller's
// subsequent os.Open. Hits do not touch modification times or generation order.
func (c *fileCache) acquire(name string) (fileCacheLease, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[name]
	path := filepath.Join(c.dir, name)
	if e != nil {
		path = e.path
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if e != nil {
			c.bytes -= e.size
			delete(c.entries, name)
		}
		return fileCacheLease{}, false, nil
	}
	if err != nil {
		return fileCacheLease{}, false, err
	}
	if !info.Mode().IsRegular() {
		return fileCacheLease{}, false, fmt.Errorf("cache entry %s is not a regular file", path)
	}
	if e == nil {
		e = c.recordLocked(name, path, info)
	}
	lease := c.leaseLocked(e)
	c.cleanupLocked()
	return lease, true, nil
}

func (c *fileCache) leaseLocked(e *fileCacheEntry) fileCacheLease {
	e.users++
	var once sync.Once
	return fileCacheLease{path: e.path, release: func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			e.users--
			c.cleanupLocked()
		})
	}}
}

func (c *fileCache) getOrCreate(name string, generate func(string) error) (fileCacheLease, error) {
	if filepath.Base(name) != name || !c.owned(name) {
		return fileCacheLease{}, fmt.Errorf("invalid cache filename %q", name)
	}
	if lease, ok, err := c.acquire(name); ok || err != nil {
		return lease, err
	}
	unlock := c.lockName(name)
	defer unlock()
	if lease, ok, err := c.acquire(name); ok || err != nil {
		return lease, err
	}

	// Generate off to the side so initialization and eviction cannot observe a
	// completed filename before it has accounting and a serving reference.
	tmp, err := os.CreateTemp(c.dir, ".file-cache-*")
	if err != nil {
		return fileCacheLease{}, err
	}
	path := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(path)
		return fileCacheLease{}, err
	}
	defer func() {
		if path != "" {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Printf("file cache: remove generation temporary file %s: %v", path, err)
			}
		}
	}()
	if err := generate(path); err != nil {
		return fileCacheLease{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fileCacheLease{}, err
	}
	if !info.Mode().IsRegular() {
		return fileCacheLease{}, fmt.Errorf("generated cache entry %s is not a regular file", path)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	finalPath := filepath.Join(c.dir, name)
	if _, err := os.Lstat(finalPath); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return fileCacheLease{}, err
		}
		return fileCacheLease{}, fmt.Errorf("cache entry appeared during generation: %s", finalPath)
	}
	oversized := info.Size() > c.limit
	if !oversized {
		if err := os.Rename(path, finalPath); err != nil {
			return fileCacheLease{}, err
		}
	} else {
		finalPath = path
	}
	e := c.recordLocked(name, finalPath, info)
	e.disposable = oversized
	c.pending = c.pending || oversized
	path = "" // The cache now owns cleanup, including oversized temporary files.
	lease := c.leaseLocked(e)
	c.cleanupLocked()
	return lease, nil
}

func (c *fileCache) lockName(name string) func() {
	c.mu.Lock()
	lock := c.locks[name]
	if lock == nil {
		lock = &fileCacheLock{}
		c.locks[name] = lock
	}
	lock.users++
	c.mu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		c.mu.Lock()
		defer c.mu.Unlock()
		lock.users--
		if lock.users == 0 {
			delete(c.locks, name)
		}
	}
}

func (c *fileCache) cleanupLocked() {
	if c.bytes <= c.limit && !c.pending {
		return
	}
	entries := make([]*fileCacheEntry, 0, len(c.entries))
	for _, e := range c.entries {
		if e.users == 0 {
			entries = append(entries, e)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].generated.Equal(entries[j].generated) {
			return entries[i].name < entries[j].name
		}
		return entries[i].generated.Before(entries[j].generated)
	})
	for _, e := range entries {
		if c.bytes <= c.limit && !e.disposable && e.size <= c.limit {
			continue
		}
		// Never remove a symlink or directory supplied externally.
		info, err := os.Lstat(e.path)
		if err == nil && !info.Mode().IsRegular() {
			log.Printf("file cache: cannot evict non-regular entry %s", e.path)
			continue
		}
		if err == nil {
			err = c.remove(e.path)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("file cache: evict %s: %v", e.path, err)
			continue
		}
		c.bytes -= e.size
		delete(c.entries, e.name)
	}
	c.pending = c.bytes > c.limit
	for _, e := range c.entries {
		c.pending = c.pending || e.disposable
	}
}

//go:build linux && !arm

package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"replica/internal/apiclient"
	"replica/internal/config"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

func TestSanitizeShareName(t *testing.T) {
	cases := map[string]string{
		"My Album":            "My-Album",
		"a/b/c":               "a-b-c",
		"weird!!name":         "weird-name",
		"keep.dots_and-dash":  "keep.dots_and-dash",
		"  leading trailing ": "leading-trailing",
		"...":                 "",
		"..":                  "",
		"/":                   "",
		"café":                "caf",
	}
	for input, want := range cases {
		if got := sanitizeShareName(input); got != want {
			t.Fatalf("sanitizeShareName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFuseMountPointCollision(t *testing.T) {
	m := &fuseManager{cfg: config.FuseConfig{PathPrefix: "/mnt/shares"}}
	used := map[string]uint{}

	first := m.mountPoint(apiclient.Share{ID: 1, Name: "My Album"}, used)
	used[first] = 1
	second := m.mountPoint(apiclient.Share{ID: 2, Name: "My Album"}, used)

	if first != "/mnt/shares/My-Album" {
		t.Fatalf("first mount point = %q", first)
	}
	if second != "/mnt/shares/My-Album-2" {
		t.Fatalf("collision mount point = %q, want /mnt/shares/My-Album-2", second)
	}
}

func TestFuseMountPointEmptyNameFallsBackToID(t *testing.T) {
	m := &fuseManager{cfg: config.FuseConfig{PathPrefix: "/mnt/shares"}}
	used := map[string]uint{}
	got := m.mountPoint(apiclient.Share{ID: 9, Name: "..."}, used)
	if got != "/mnt/shares/share-9" {
		t.Fatalf("mount point = %q, want /mnt/shares/share-9", got)
	}
}

func newFuseTestRuntime() *Runtime {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return &Runtime{
		replicaFiles: map[uint][]apiclient.ReplicaInventoryFile{
			7: {
				{FileID: 1, ReplicaID: 7, RelativeURI: "top.txt", Size: 4, InventoryStatus: "active", ReplicaStatus: "synchronized", Modified: now, Created: now},
				{FileID: 2, ReplicaID: 7, RelativeURI: "album/photo.jpg", Size: 10, InventoryStatus: "active", ReplicaStatus: "synchronized", Modified: now, Created: now},
				{FileID: 3, ReplicaID: 7, RelativeURI: "album/nested/deep.txt", Size: 20, InventoryStatus: "active", ReplicaStatus: "synchronized", Modified: now, Created: now},
				{FileID: 4, ReplicaID: 7, RelativeURI: "pending.txt", Size: 5, InventoryStatus: "active", ReplicaStatus: "pending"},
			},
		},
	}
}

func TestShareFSTreeChildren(t *testing.T) {
	s := newShareFS(newFuseTestRuntime(), 7, "file:///replica", nil)

	root := s.children("")
	if !hasChild(root, "top.txt", false) || !hasChild(root, "album", true) {
		t.Fatalf("root children = %+v, want top.txt(file) and album(dir)", root)
	}
	if hasChild(root, "pending.txt", false) {
		t.Fatalf("pending file should be excluded from tree: %+v", root)
	}

	album := s.children("album")
	if !hasChild(album, "photo.jpg", false) || !hasChild(album, "nested", true) {
		t.Fatalf("album children = %+v, want photo.jpg(file) and nested(dir)", album)
	}
}

func TestShareFSLookupAndHasUnder(t *testing.T) {
	s := newShareFS(newFuseTestRuntime(), 7, "file:///replica", nil)

	if _, ok := s.lookupFile("album/photo.jpg"); !ok {
		t.Fatal("expected album/photo.jpg to resolve as a file")
	}
	if _, ok := s.lookupFile("album"); ok {
		t.Fatal("album should not resolve as a file")
	}
	if !s.hasUnder("album/") {
		t.Fatal("expected hasUnder(album/) to be true")
	}
	if s.hasUnder("missing/") {
		t.Fatal("expected hasUnder(missing/) to be false")
	}
}

func TestShareFSInoStableAndUnique(t *testing.T) {
	s := newShareFS(newFuseTestRuntime(), 7, "file:///replica", nil)
	a := s.ino("album/photo.jpg")
	b := s.ino("album/photo.jpg")
	c := s.ino("top.txt")
	if a != b {
		t.Fatalf("inode for same path not stable: %d vs %d", a, b)
	}
	if a == c {
		t.Fatalf("different paths share inode: %d", a)
	}
	if a < 2 || c < 2 {
		t.Fatalf("inode numbers must avoid reserved values: %d %d", a, c)
	}
}

func TestShareFSFileAndDirNameCollision(t *testing.T) {
	r := &Runtime{replicaFiles: map[uint][]apiclient.ReplicaInventoryFile{
		7: {
			{FileID: 1, RelativeURI: "album", InventoryStatus: "active", ReplicaStatus: "synchronized"},
			{FileID: 2, RelativeURI: "album/photo.jpg", InventoryStatus: "active", ReplicaStatus: "synchronized"},
		},
	}}
	s := newShareFS(r, 7, "file:///replica", nil)
	children := s.children("")
	if !hasChild(children, "album", false) {
		t.Fatalf("exact file should win over directory for name collision: %+v", children)
	}
}

func TestReadHandleRangedRead(t *testing.T) {
	h := &readHandle{
		size:        10,
		rangeReader: &fakeRangeReader{data: "0123456789"},
		uri:         "s3://bucket/key",
		rel:         "file.txt",
	}

	res, errno := h.Read(context.Background(), make([]byte, 4), 3)
	if errno != 0 {
		t.Fatalf("read errno = %v", errno)
	}
	data, status := readResultBytes(res)
	if status != fuse.OK {
		t.Fatalf("read result status = %v", status)
	}
	if string(data) != "3456" {
		t.Fatalf("read data = %q, want 3456", string(data))
	}
}

func TestReadHandleClampsPastEOF(t *testing.T) {
	h := &readHandle{
		size:        5,
		rangeReader: &fakeRangeReader{data: "01234"},
		uri:         "s3://bucket/key",
		rel:         "file.txt",
	}

	res, errno := h.Read(context.Background(), make([]byte, 8), 2)
	if errno != 0 {
		t.Fatalf("read errno = %v", errno)
	}
	data, status := readResultBytes(res)
	if status != fuse.OK {
		t.Fatalf("read result status = %v", status)
	}
	if string(data) != "234" {
		t.Fatalf("read data = %q, want 234", string(data))
	}
}

type fakeRangeReader struct{ data string }

func (f *fakeRangeReader) OpenRange(_ context.Context, _ string, _ string, offset int64, length int64) (io.ReadCloser, error) {
	if offset >= int64(len(f.data)) {
		return io.NopCloser(strings.NewReader("")), nil
	}
	end := offset + length
	if end > int64(len(f.data)) {
		end = int64(len(f.data))
	}
	return io.NopCloser(strings.NewReader(f.data[offset:end])), nil
}

func readResultBytes(res fuse.ReadResult) ([]byte, fuse.Status) {
	data, status := res.Bytes(nil)
	return data, status
}

func hasChild(children []shareChild, name string, isDir bool) bool {
	for _, child := range children {
		if child.name == name && child.isDir == isDir {
			return true
		}
	}
	return false
}

func TestShareFSFuseMountIntegration(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse")
	}

	backing := t.TempDir()
	if err := os.MkdirAll(filepath.Join(backing, "album"), 0o755); err != nil {
		t.Fatalf("mkdir backing: %v", err)
	}
	if err := os.WriteFile(filepath.Join(backing, "top.txt"), []byte("top"), 0o644); err != nil {
		t.Fatalf("write top.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(backing, "album", "photo.jpg"), []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write photo.jpg: %v", err)
	}

	now := time.Now()
	r := &Runtime{replicaFiles: map[uint][]apiclient.ReplicaInventoryFile{
		7: {
			{FileID: 1, ReplicaID: 7, RelativeURI: "top.txt", Size: 3, InventoryStatus: "active", ReplicaStatus: "synchronized", Modified: now, Created: now},
			{FileID: 2, ReplicaID: 7, RelativeURI: "album/photo.jpg", Size: int64(len("hello world")), InventoryStatus: "active", ReplicaStatus: "synchronized", Modified: now, Created: now},
		},
	}}
	s := newShareFS(r, 7, backing, nil)

	mountPoint := t.TempDir()
	root := &shareDirNode{prefix: "", s: s}
	entryTimeout := time.Second
	attrTimeout := time.Second
	server, err := fs.Mount(mountPoint, root, &fs.Options{
		MountOptions: fuse.MountOptions{
			Options: []string{"ro"},
			FsName:  "replica-share-test",
			Name:    "replica-share-test",
		},
		EntryTimeout: &entryTimeout,
		AttrTimeout:  &attrTimeout,
	})
	if err != nil {
		t.Skipf("fuse mount unavailable: %v", err)
	}
	defer func() {
		if err := server.Unmount(); err != nil {
			t.Logf("unmount failed: %v", err)
		}
	}()

	data, err := os.ReadFile(filepath.Join(mountPoint, "top.txt"))
	if err != nil {
		t.Fatalf("read top.txt: %v", err)
	}
	if string(data) != "top" {
		t.Fatalf("top.txt = %q, want top", string(data))
	}

	data, err = os.ReadFile(filepath.Join(mountPoint, "album", "photo.jpg"))
	if err != nil {
		t.Fatalf("read album/photo.jpg: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("album/photo.jpg = %q, want hello world", string(data))
	}

	entries, err := os.ReadDir(mountPoint)
	if err != nil {
		t.Fatalf("read mount dir: %v", err)
	}
	children := make([]shareChild, 0, len(entries))
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
		children = append(children, shareChild{name: entry.Name(), isDir: entry.IsDir()})
	}
	if !hasChild(children, "top.txt", false) || !hasChild(children, "album", true) {
		t.Fatalf("mount entries = %v, want top.txt and album", names)
	}
}

func TestFuseReconcileSkipsSharesNotEnabledForFuse(t *testing.T) {
	client, err := apiclient.New(config.Config{App: config.AppConfig{NodeID: "node", NodeAddress: "http://127.0.0.1", CoordinatorURL: "http://127.0.0.1"}, Auth: config.AuthConfig{NodeSecret: "test"}})
	if err != nil {
		t.Fatalf("apiclient.New() error = %v", err)
	}
	enabled, disabled := true, false
	cases := map[string]struct {
		sharingEnabled bool
		fuseEnabled    *bool
		pathPrefix     bool
	}{
		"sharing disabled":    {sharingEnabled: false, fuseEnabled: &enabled, pathPrefix: true},
		"fuse unset":          {sharingEnabled: true, fuseEnabled: nil, pathPrefix: true},
		"fuse disabled":       {sharingEnabled: true, fuseEnabled: &disabled, pathPrefix: true},
		"path prefix not set": {sharingEnabled: true, fuseEnabled: &enabled, pathPrefix: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			prefix := t.TempDir()
			cfg := config.Config{}
			cfg.Sharing.Enabled = tc.sharingEnabled
			r := &Runtime{
				client:   client,
				cfg:      cfg,
				replicas: []apiclient.Replica{{ID: 7, NodeID: "node", Status: "active"}},
				shares:   []apiclient.Share{{ID: 1, ReplicaID: 7, Name: "Album", Status: "active", Properties: apiclient.ShareProperties{FuseEnabled: tc.fuseEnabled}}},
			}
			m := &fuseManager{runtime: r, mounts: make(map[uint]*fuseMount)}
			if tc.pathPrefix {
				m.cfg.PathPrefix = prefix
			}

			m.reconcile(context.Background())

			if len(m.mounts) != 0 {
				t.Fatalf("mounts = %v, want none", m.mounts)
			}
			entries, err := os.ReadDir(prefix)
			if err != nil {
				t.Fatalf("ReadDir() error = %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("mount point entries = %d, want none", len(entries))
			}
		})
	}
}

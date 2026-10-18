package storage

import (
	"context"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"

	"replica/internal/apiclient"
	"replica/internal/config"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// shareFS is the read-only view of a single share's replica files. It derives
// its directory tree on demand from the storage runtime state, so listings and
// attributes always reflect the latest synchronized inventory.
type shareFS struct {
	replicaID uint
	uri       string
	profile   *config.StorageProfileConfig
	runtime   *Runtime

	inoMu  sync.Mutex
	inoMap map[string]uint64
	inoSeq uint64
}

func newShareFS(r *Runtime, replicaID uint, uri string, profile *config.StorageProfileConfig) *shareFS {
	return &shareFS{
		replicaID: replicaID,
		uri:       uri,
		profile:   profile,
		runtime:   r,
		inoMap:    make(map[string]uint64),
	}
}

func (s *shareFS) files() []apiclient.ReplicaInventoryFile {
	return s.runtime.availableShareFiles(s.replicaID)
}

func (s *shareFS) ino(rel string) uint64 {
	s.inoMu.Lock()
	defer s.inoMu.Unlock()
	if value, ok := s.inoMap[rel]; ok {
		return value
	}
	s.inoSeq++
	value := s.inoSeq + 1
	s.inoMap[rel] = value
	return value
}

func (s *shareFS) lookupFile(rel string) (apiclient.ReplicaInventoryFile, bool) {
	for _, file := range s.files() {
		if file.RelativeURI == rel {
			return file, true
		}
	}
	return apiclient.ReplicaInventoryFile{}, false
}

func (s *shareFS) hasUnder(prefix string) bool {
	for _, file := range s.files() {
		if strings.HasPrefix(file.RelativeURI, prefix) {
			return true
		}
	}
	return false
}

type shareChild struct {
	name  string
	isDir bool
}

func (s *shareFS) children(prefix string) []shareChild {
	exact := make(map[string]struct{})
	dirs := make(map[string]struct{})
	for _, file := range s.files() {
		rel := file.RelativeURI
		var rest string
		if prefix == "" {
			rest = rel
		} else {
			if !strings.HasPrefix(rel, prefix+"/") {
				continue
			}
			rest = rel[len(prefix)+1:]
		}
		if rest == "" {
			continue
		}
		segment, _, hasMore := cutFirstSegment(rest)
		if segment == "" {
			continue
		}
		if hasMore {
			dirs[segment] = struct{}{}
		} else {
			exact[segment] = struct{}{}
		}
	}

	children := make([]shareChild, 0, len(exact)+len(dirs))
	for name := range exact {
		children = append(children, shareChild{name: name, isDir: false})
	}
	for name := range dirs {
		if _, conflict := exact[name]; conflict {
			continue
		}
		children = append(children, shareChild{name: name, isDir: true})
	}
	sort.Slice(children, func(i, j int) bool { return children[i].name < children[j].name })
	return children
}

func cutFirstSegment(rest string) (string, string, bool) {
	idx := strings.IndexByte(rest, '/')
	if idx < 0 {
		return rest, "", false
	}
	return rest[:idx], rest[idx+1:], true
}

func fillDirAttr(attr *fuse.Attr) {
	attr.Mode = fuse.S_IFDIR | 0555
	attr.Nlink = 2
	attr.Size = 4096
}

func fillFileAttr(attr *fuse.Attr, file apiclient.ReplicaInventoryFile) {
	attr.Mode = fuse.S_IFREG | 0444
	attr.Nlink = 1
	attr.Size = uint64(file.Size)
	attr.Mtime = uint64(file.Modified.Unix())
	attr.Mtimensec = uint32(file.Modified.Nanosecond())
	attr.Atime = uint64(file.Created.Unix())
	attr.Atimensec = uint32(file.Created.Nanosecond())
	attr.Ctime = uint64(file.Created.Unix())
	attr.Ctimensec = uint32(file.Created.Nanosecond())
}

type shareDirNode struct {
	fs.Inode
	prefix string
	s      *shareFS
}

var _ = (fs.NodeLookuper)((*shareDirNode)(nil))
var _ = (fs.NodeReaddirer)((*shareDirNode)(nil))
var _ = (fs.NodeGetattrer)((*shareDirNode)(nil))

func (n *shareDirNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	fillDirAttr(&out.Attr)
	return 0
}

func (n *shareDirNode) Readdir(_ context.Context) (fs.DirStream, syscall.Errno) {
	children := n.s.children(n.prefix)
	entries := make([]fuse.DirEntry, 0, len(children))
	for _, child := range children {
		mode := uint32(fuse.S_IFREG)
		if child.isDir {
			mode = fuse.S_IFDIR
		}
		entries = append(entries, fuse.DirEntry{
			Name: child.name,
			Mode: mode,
			Ino:  n.s.ino(path.Join(n.prefix, child.name)),
		})
	}
	return fs.NewListDirStream(entries), 0
}

func (n *shareDirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if name == "" || name == "." || name == ".." {
		return nil, syscall.EINVAL
	}
	childRel := path.Join(n.prefix, name)
	if file, ok := n.s.lookupFile(childRel); ok {
		fillFileAttr(&out.Attr, file)
		node := &shareFileNode{path: childRel, s: n.s}
		return n.NewInode(ctx, node, fs.StableAttr{Mode: fuse.S_IFREG, Ino: n.s.ino(childRel)}), 0
	}
	if n.s.hasUnder(childRel + "/") {
		fillDirAttr(&out.Attr)
		node := &shareDirNode{prefix: childRel, s: n.s}
		return n.NewInode(ctx, node, fs.StableAttr{Mode: fuse.S_IFDIR, Ino: n.s.ino(childRel)}), 0
	}
	return nil, syscall.ENOENT
}

type shareFileNode struct {
	fs.Inode
	path string
	s    *shareFS
}

var _ = (fs.NodeGetattrer)((*shareFileNode)(nil))
var _ = (fs.NodeOpener)((*shareFileNode)(nil))

func (n *shareFileNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	file, ok := n.s.lookupFile(n.path)
	if !ok {
		return syscall.ENOENT
	}
	fillFileAttr(&out.Attr, file)
	return 0
}

func (n *shareFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_APPEND|syscall.O_TRUNC) != 0 {
		return nil, 0, syscall.EROFS
	}
	file, ok := n.s.lookupFile(n.path)
	if !ok {
		return nil, 0, syscall.ENOENT
	}
	reader, err := GetReader(ctx, n.s.uri, n.s.profile)
	if err != nil {
		return nil, 0, fs.ToErrno(err)
	}
	if rangeReader, ok := reader.(RangeReader); ok {
		return &readHandle{size: file.Size, rangeReader: rangeReader, uri: n.s.uri, rel: n.path}, 0, 0
	}
	rc, _, err := reader.Open(ctx, n.s.uri, n.path)
	if err != nil {
		return nil, 0, fs.ToErrno(err)
	}
	readerAt, ok := rc.(io.ReaderAt)
	if !ok {
		_ = rc.Close()
		return nil, 0, syscall.EIO
	}
	return &readHandle{size: file.Size, readerAt: readerAt, closer: rc}, 0, 0
}

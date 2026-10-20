//go:build linux && !arm

package storage

import (
	"context"
	"io"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// readHandle serves random-access reads for a single opened share file.
// Seekable readers (filesystem, sftp) reuse an io.ReaderAt handle; S3 is
// served through ranged GetObject calls.
type readHandle struct {
	size        int64
	readerAt    io.ReaderAt
	closer      io.Closer
	rangeReader RangeReader
	uri         string
	rel         string
}

var _ = (fs.FileHandle)((*readHandle)(nil))

func (h *readHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if off >= h.size {
		return fuse.ReadResultData(nil), 0
	}
	length := int64(len(dest))
	if off+length > h.size {
		length = h.size - off
	}
	buf := dest[:length]

	var read int
	var err error
	if h.readerAt != nil {
		read, err = h.readerAt.ReadAt(buf, off)
	} else {
		read, err = readRange(ctx, h.rangeReader, h.uri, h.rel, off, buf)
	}
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, fs.ToErrno(err)
	}
	if read < 0 {
		read = 0
	}
	if int64(read) > length {
		read = int(length)
	}
	return fuse.ReadResultData(buf[:read]), 0
}

func (h *readHandle) Release(_ context.Context) syscall.Errno {
	if h.closer != nil {
		if err := h.closer.Close(); err != nil {
			return fs.ToErrno(err)
		}
	}
	return 0
}

func readRange(ctx context.Context, rangeReader RangeReader, uri string, rel string, off int64, buf []byte) (int, error) {
	rc, err := rangeReader.OpenRange(ctx, uri, rel, off, int64(len(buf)))
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	read, err := io.ReadFull(rc, buf)
	if err == io.ErrUnexpectedEOF {
		err = nil
	}
	return read, err
}

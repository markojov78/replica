package storage

import (
	"context"
	"io"
	"os"
	"path"

	"github.com/pkg/sftp"
)

type SFTPReader struct{ connector *sftpConnector }

type sftpReadCloser struct {
	*sftp.File
	connection *sftpConnection
}

func (r *sftpReadCloser) Close() error { err := r.File.Close(); _ = r.connection.Close(); return err }

func (r *SFTPReader) Open(ctx context.Context, uri, rel string) (io.ReadCloser, int64, error) {
	rel, err := cleanWriteRelativeURI(rel)
	if err != nil {
		return nil, 0, err
	}
	c, root, err := r.connector.connect(ctx, uri)
	if err != nil {
		return nil, 0, err
	}
	fail := func(err error) (io.ReadCloser, int64, error) {
		c.Close()
		if os.IsNotExist(err) {
			err = errTransferFileNotFound
		}
		return nil, 0, err
	}
	if err := sftpRoot(c.Client, root); err != nil {
		return fail(err)
	}
	if err := sftpParents(c.Client, root, rel, false); err != nil {
		return fail(err)
	}
	f, err := c.Open(path.Join(root, rel))
	if err != nil {
		return fail(err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fail(err)
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return fail(errTransferFileNotFound)
	}
	return &sftpReadCloser{File: f, connection: c}, info.Size(), nil
}

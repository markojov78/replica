package storage

import (
	"context"
	"io"
)

type sftpThumbnailSource struct {
	reader           Reader
	uri, relativeURI string
	size             int64
}

func (s *sftpThumbnailSource) Open(ctx context.Context) (io.ReadCloser, error) {
	r, _, err := s.reader.Open(ctx, s.uri, s.relativeURI)
	return r, err
}
func (s *sftpThumbnailSource) Name() string      { return s.relativeURI }
func (s *sftpThumbnailSource) Size() int64       { return s.size }
func (s *sftpThumbnailSource) IsLocalFile() bool { return false }
func (s *sftpThumbnailSource) LocalPath() string { return "" }

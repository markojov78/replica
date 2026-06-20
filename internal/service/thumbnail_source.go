package service

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type ThumbnailSource interface {
	Open(ctx context.Context) (io.ReadCloser, error)
	Name() string
	Size() int64
	IsLocalFile() bool
	LocalPath() string
}

type LocalFileThumbnailSource struct {
	Path        string
	DisplayName string
}

func NewLocalFileThumbnailSource(path string, displayName string) LocalFileThumbnailSource {
	return LocalFileThumbnailSource{Path: path, DisplayName: displayName}
}

func (s LocalFileThumbnailSource) Open(ctx context.Context) (io.ReadCloser, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return os.Open(s.Path)
}

func (s LocalFileThumbnailSource) Name() string {
	name := strings.TrimSpace(s.DisplayName)
	if name != "" {
		return name
	}
	return filepath.Base(s.Path)
}

func (s LocalFileThumbnailSource) Size() int64 {
	info, err := os.Stat(s.Path)
	if err != nil || info.IsDir() {
		return -1
	}
	return info.Size()
}

func (s LocalFileThumbnailSource) IsLocalFile() bool {
	return true
}

func (s LocalFileThumbnailSource) LocalPath() string {
	return s.Path
}

type thumbnailS3GetObjectAPI interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

type S3ThumbnailSource struct {
	client      thumbnailS3GetObjectAPI
	bucket      string
	key         string
	displayName string
	size        int64
}

func NewS3ThumbnailSource(client thumbnailS3GetObjectAPI, bucket string, key string, displayName string, size int64) S3ThumbnailSource {
	return S3ThumbnailSource{
		client:      client,
		bucket:      bucket,
		key:         key,
		displayName: displayName,
		size:        size,
	}
}

func (s S3ThumbnailSource) Open(ctx context.Context) (io.ReadCloser, error) {
	if s.client == nil || strings.TrimSpace(s.bucket) == "" || strings.TrimSpace(s.key) == "" {
		return nil, ErrThumbnailSourceMissing
	}
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key),
	})
	if err != nil {
		return nil, err
	}
	if output == nil || output.Body == nil {
		return nil, ErrThumbnailSourceMissing
	}
	return output.Body, nil
}

func (s S3ThumbnailSource) Name() string {
	name := strings.TrimSpace(s.displayName)
	if name != "" {
		return name
	}
	return path.Base(s.key)
}

func (s S3ThumbnailSource) Size() int64 {
	return s.size
}

func (s S3ThumbnailSource) IsLocalFile() bool {
	return false
}

func (s S3ThumbnailSource) LocalPath() string {
	return ""
}

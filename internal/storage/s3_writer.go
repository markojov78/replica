package storage

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zeebo/blake3"
)

type s3PutObjectAPI interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

type s3DeleteObjectAPI interface {
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

type S3Writer struct {
	putClient    s3PutObjectAPI
	deleteClient s3DeleteObjectAPI
}

func NewS3Writer(client *s3.Client) *S3Writer {
	return &S3Writer{
		putClient:    client,
		deleteClient: client,
	}
}

func NewS3WriterWithClients(putClient s3PutObjectAPI, deleteClient s3DeleteObjectAPI) *S3Writer {
	return &S3Writer{
		putClient:    putClient,
		deleteClient: deleteClient,
	}
}

func (w *S3Writer) Save(ctx context.Context, replicaURI string, relativeURI string, content io.Reader, size int64) error {
	location, key, err := resolveS3WriteKey(replicaURI, relativeURI)
	if err != nil {
		return err
	}

	body, cleanup, err := seekableS3UploadBody(ctx, content)
	if err != nil {
		return err
	}
	defer cleanup()

	input := &s3.PutObjectInput{
		Bucket: aws.String(location.Bucket),
		Key:    aws.String(key),
		Body:   body,
	}
	if size >= 0 {
		input.ContentLength = aws.Int64(size)
	}
	_, err = w.putClient.PutObject(ctx, input)
	return err
}

func (w *S3Writer) SaveVerified(ctx context.Context, replicaURI string, relativeURI string, content io.Reader, expectedSize int64, expectedHash string) error {
	if _, _, err := resolveS3WriteKey(replicaURI, relativeURI); err != nil {
		return err
	}

	hasher := blake3.New()
	// TeeReader deliberately hides any seeker: upload the exact staged bytes
	// that were verified, even if the original source can change.
	body, cleanup, err := seekableS3UploadBody(ctx, io.TeeReader(content, hasher))
	if err != nil {
		return err
	}
	defer cleanup()

	written, err := body.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if written != expectedSize || actualHash != expectedHash {
		return fmt.Errorf("%w: expected size=%d hash=%s, got size=%d hash=%s",
			ErrFileIntegrityMismatch, expectedSize, expectedHash, written, actualHash)
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return w.Save(ctx, replicaURI, relativeURI, body, expectedSize)
}

func (w *S3Writer) Delete(ctx context.Context, replicaURI string, relativeURI string) error {
	location, key, err := resolveS3WriteKey(replicaURI, relativeURI)
	if err != nil {
		return err
	}

	_, err = w.deleteClient.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(location.Bucket),
		Key:    aws.String(key),
	})
	return err
}

func resolveS3WriteKey(replicaURI string, relativeURI string) (s3Location, string, error) {
	location, err := parseS3URI(replicaURI)
	if err != nil {
		return s3Location{}, "", err
	}

	cleanRelative, err := cleanWriteRelativeURI(relativeURI)
	if err != nil {
		return s3Location{}, "", err
	}

	if location.Prefix == "" {
		return location, cleanRelative, nil
	}
	return location, strings.Trim(path.Join(location.Prefix, cleanRelative), "/"), nil
}

func seekableS3UploadBody(ctx context.Context, content io.Reader) (io.ReadSeeker, func(), error) {
	if seeker, ok := content.(io.ReadSeeker); ok {
		return seeker, func() {}, nil
	}

	tempFile, err := os.CreateTemp("", temporaryWritePattern())
	if err != nil {
		return nil, nil, err
	}
	tempPath := tempFile.Name()
	cleanup := func() {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
	}

	if _, err := copyWithContext(ctx, tempFile, content); err != nil {
		cleanup()
		return nil, nil, err
	}
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, nil, err
	}
	return tempFile, cleanup, nil
}

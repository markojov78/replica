package storage

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type s3GetObjectAPI interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

type S3Reader struct {
	client s3GetObjectAPI
}

func NewS3Reader(client *s3.Client) *S3Reader {
	return &S3Reader{client: client}
}

func NewS3ReaderWithClient(client s3GetObjectAPI) *S3Reader {
	return &S3Reader{client: client}
}

func (r *S3Reader) Open(ctx context.Context, replicaURI string, relativeURI string) (io.ReadCloser, int64, error) {
	location, key, err := resolveS3ReadKey(replicaURI, relativeURI)
	if err != nil {
		return nil, 0, err
	}

	output, err := r.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(location.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, 0, err
	}

	return output.Body, aws.ToInt64(output.ContentLength), nil
}

func (r *S3Reader) OpenRange(ctx context.Context, replicaURI string, relativeURI string, offset int64, length int64) (io.ReadCloser, error) {
	location, key, err := resolveS3ReadKey(replicaURI, relativeURI)
	if err != nil {
		return nil, err
	}
	if offset < 0 || length <= 0 {
		return nil, fmt.Errorf("invalid s3 range offset=%d length=%d", offset, length)
	}

	output, err := r.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(location.Bucket),
		Key:    aws.String(key),
		Range:  aws.String(fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)),
	})
	if err != nil {
		return nil, err
	}

	return output.Body, nil
}

func resolveS3ReadKey(replicaURI string, relativeURI string) (s3Location, string, error) {
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

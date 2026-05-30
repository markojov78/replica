package storage

import (
	"context"
	"io"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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

func (w *S3Writer) Save(ctx context.Context, replicaURI string, relativeURI string, content io.Reader) error {
	location, key, err := resolveS3WriteKey(replicaURI, relativeURI)
	if err != nil {
		return err
	}

	_, err = w.putClient.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(location.Bucket),
		Key:    aws.String(key),
		Body:   content,
	})
	return err
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

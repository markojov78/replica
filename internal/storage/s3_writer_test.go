package storage

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestS3WriterSaveAndDeleteUseResolvedKey(t *testing.T) {
	putClient := &mockS3PutClient{}
	deleteClient := &mockS3DeleteClient{}
	writer := NewS3WriterWithClients(putClient, deleteClient)

	if err := writer.Save(context.Background(), "s3://bucket/root/prefix", "nested/file.txt", strings.NewReader("content"), int64(len("content"))); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if putClient.bucket != "bucket" || putClient.key != "root/prefix/nested/file.txt" {
		t.Fatalf("put target = %s/%s, want bucket/root/prefix/nested/file.txt", putClient.bucket, putClient.key)
	}
	if putClient.contentLength != int64(len("content")) {
		t.Fatalf("put contentLength = %d, want %d", putClient.contentLength, len("content"))
	}
	if putClient.body != "content" {
		t.Fatalf("put body = %q, want content", putClient.body)
	}

	if err := writer.Delete(context.Background(), "s3://bucket/root/prefix", "nested/file.txt"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if deleteClient.bucket != "bucket" || deleteClient.key != "root/prefix/nested/file.txt" {
		t.Fatalf("delete target = %s/%s, want bucket/root/prefix/nested/file.txt", deleteClient.bucket, deleteClient.key)
	}
}

func TestS3WriterRejectsPathTraversal(t *testing.T) {
	writer := NewS3WriterWithClients(&mockS3PutClient{}, &mockS3DeleteClient{})
	if err := writer.Save(context.Background(), "s3://bucket/root", "../escape.txt", strings.NewReader("content"), int64(len("content"))); err == nil {
		t.Fatal("Save() error = nil, want path traversal rejection")
	}
}

type mockS3PutClient struct {
	bucket        string
	key           string
	body          string
	contentLength int64
	err           error
}

func (m *mockS3PutClient) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	m.bucket = aws.ToString(input.Bucket)
	m.key = aws.ToString(input.Key)
	m.contentLength = aws.ToInt64(input.ContentLength)
	if input.Body != nil {
		data, _ := io.ReadAll(input.Body)
		m.body = string(data)
	}
	return &s3.PutObjectOutput{}, m.err
}

type mockS3DeleteClient struct {
	bucket string
	key    string
	err    error
}

func (m *mockS3DeleteClient) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	m.bucket = aws.ToString(input.Bucket)
	m.key = aws.ToString(input.Key)
	return &s3.DeleteObjectOutput{}, m.err
}

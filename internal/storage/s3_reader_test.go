package storage

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestS3ReaderOpenUsesResolvedKey(t *testing.T) {
	client := &mockS3GetClient{body: "content", contentLength: int64(len("content"))}
	reader := NewS3ReaderWithClient(client)

	body, size, err := reader.Open(context.Background(), "s3://bucket/root/prefix", "nested/file.txt")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer body.Close()

	if client.bucket != "bucket" || client.key != "root/prefix/nested/file.txt" {
		t.Fatalf("get target = %s/%s, want bucket/root/prefix/nested/file.txt", client.bucket, client.key)
	}
	if size != int64(len("content")) {
		t.Fatalf("size = %d, want %d", size, len("content"))
	}
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(data) != "content" {
		t.Fatalf("body = %q, want content", string(data))
	}
}

func TestS3ReaderOpenRangeUsesRangeHeader(t *testing.T) {
	client := &mockS3GetClient{body: "content", contentLength: int64(len("content"))}
	reader := NewS3ReaderWithClient(client)

	body, err := reader.OpenRange(context.Background(), "s3://bucket/root/prefix", "nested/file.txt", 2, 3)
	if err != nil {
		t.Fatalf("OpenRange() error = %v", err)
	}
	defer body.Close()

	if client.bucket != "bucket" || client.key != "root/prefix/nested/file.txt" {
		t.Fatalf("get target = %s/%s, want bucket/root/prefix/nested/file.txt", client.bucket, client.key)
	}
	if client.rangeValue != "bytes=2-4" {
		t.Fatalf("range = %q, want bytes=2-4", client.rangeValue)
	}
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(data) != "content" {
		t.Fatalf("body = %q, want content", string(data))
	}
}

func TestS3ReaderOpenRangeRejectsInvalidRange(t *testing.T) {
	client := &mockS3GetClient{body: "content", contentLength: 7}
	reader := NewS3ReaderWithClient(client)

	if _, err := reader.OpenRange(context.Background(), "s3://bucket/key", "file.txt", 0, 0); err == nil {
		t.Fatal("expected error for zero length range")
	}
}

type mockS3GetClient struct {
	bucket        string
	key           string
	body          string
	contentLength int64
	err           error
	rangeValue    string
}

func (m *mockS3GetClient) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	m.bucket = aws.ToString(input.Bucket)
	m.key = aws.ToString(input.Key)
	m.rangeValue = aws.ToString(input.Range)
	if m.err != nil {
		return nil, m.err
	}
	return &s3.GetObjectOutput{
		Body:          io.NopCloser(strings.NewReader(m.body)),
		ContentLength: aws.Int64(m.contentLength),
	}, nil
}

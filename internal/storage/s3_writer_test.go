package storage

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zeebo/blake3"
)

func TestS3WriterSaveVerified(t *testing.T) {
	hash := func(content string) string {
		sum := blake3.Sum256([]byte(content))
		return hex.EncodeToString(sum[:])
	}
	readErr := errors.New("source read failed")
	uploadErr := errors.New("upload failed")
	for _, tc := range []struct {
		name      string
		content   io.Reader
		size      int64
		hash      string
		canceled  bool
		putErr    error
		wantErr   error
		wantCalls int
		wantBody  string
	}{
		{name: "stream", content: io.LimitReader(strings.NewReader("content"), 7), size: 7, hash: hash("content"), wantCalls: 1, wantBody: "content"},
		{name: "seeker", content: strings.NewReader("content"), size: 7, hash: hash("content"), wantCalls: 1, wantBody: "content"},
		{name: "empty", content: strings.NewReader(""), hash: hash(""), wantCalls: 1},
		{name: "same size wrong hash", content: strings.NewReader("CONTENT"), size: 7, hash: hash("content"), wantErr: ErrFileIntegrityMismatch},
		{name: "too short", content: strings.NewReader("content"), size: 8, hash: hash("content"), wantErr: ErrFileIntegrityMismatch},
		{name: "too long", content: strings.NewReader("content"), size: 6, hash: hash("content"), wantErr: ErrFileIntegrityMismatch},
		{name: "read failure", content: io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(readErr)), size: 7, hash: hash("partial"), wantErr: readErr},
		{name: "canceled", content: strings.NewReader("content"), size: 7, hash: hash("content"), canceled: true, wantErr: context.Canceled},
		{name: "upload failure", content: strings.NewReader("content"), size: 7, hash: hash("content"), putErr: uploadErr, wantErr: uploadErr, wantCalls: 1, wantBody: "content"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			t.Setenv("TMPDIR", tempDir)
			client := &mockS3PutClient{err: tc.putErr}
			var writer verifiedWriter = NewS3WriterWithClients(client, &mockS3DeleteClient{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			err := writer.SaveVerified(ctx, "s3://bucket/root", "file.txt", tc.content, tc.size, tc.hash)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("SaveVerified() error = %v, want %v", err, tc.wantErr)
			}
			if client.calls != tc.wantCalls {
				t.Fatalf("upload calls = %d, want %d", client.calls, tc.wantCalls)
			}
			if tc.wantCalls > 0 && (client.body != tc.wantBody || client.contentLength != tc.size || client.bucket != "bucket" || client.key != "root/file.txt") {
				t.Fatalf("unexpected upload: %+v", client)
			}
			entries, err := os.ReadDir(tempDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("temporary files remain: %v", entries)
			}
		})
	}
}

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
	calls         int
	bucket        string
	key           string
	body          string
	contentLength int64
	err           error
}

func (m *mockS3PutClient) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	m.calls++
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

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestParseS3URI(t *testing.T) {
	tests := []struct {
		uri        string
		wantBucket string
		wantPrefix string
	}{
		{uri: "s3://bucket", wantBucket: "bucket", wantPrefix: ""},
		{uri: "s3://bucket/", wantBucket: "bucket", wantPrefix: ""},
		{uri: "s3://bucket/path/to/prefix", wantBucket: "bucket", wantPrefix: "path/to/prefix"},
	}

	for _, test := range tests {
		location, err := parseS3URI(test.uri)
		if err != nil {
			t.Fatalf("parseS3URI(%q) error = %v", test.uri, err)
		}
		if location.Bucket != test.wantBucket {
			t.Fatalf("parseS3URI(%q) bucket = %q, want %q", test.uri, location.Bucket, test.wantBucket)
		}
		if location.Prefix != test.wantPrefix {
			t.Fatalf("parseS3URI(%q) prefix = %q, want %q", test.uri, location.Prefix, test.wantPrefix)
		}
	}
}

func TestS3ScannerUsesObjectChecksumWhenAvailable(t *testing.T) {
	modified := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	scanner := NewS3ScannerWithClients(
		mockS3ListClient{
			output: &s3.ListObjectsV2Output{
				Contents: []types.Object{
					{
						Key:          aws.String("prefix/nested/file.txt"),
						ETag:         aws.String(`"etag-value"`),
						Size:         aws.Int64(7),
						LastModified: aws.Time(modified),
					},
				},
			},
		},
		mockS3HeadClient{
			output: &s3.HeadObjectOutput{
				ChecksumSHA256: aws.String("checksum-value"),
			},
		},
	)

	states, err := scanner.Scan(context.Background(), "s3://bucket/prefix")
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("len(states) = %d, want 1", len(states))
	}
	if states[0].RelativeURI != "nested/file.txt" {
		t.Fatalf("states[0].RelativeURI = %q, want %q", states[0].RelativeURI, "nested/file.txt")
	}
	if states[0].Hash != "checksum-value" {
		t.Fatalf("states[0].Hash = %q, want %q", states[0].Hash, "checksum-value")
	}
	if states[0].HashAlgorithm != "sha256" {
		t.Fatalf("states[0].HashAlgorithm = %q, want %q", states[0].HashAlgorithm, "sha256")
	}
	if !states[0].Modified.Equal(modified) || !states[0].Created.Equal(modified) {
		t.Fatalf("modified/created mismatch: modified=%s created=%s want=%s", states[0].Modified, states[0].Created, modified)
	}
}

func TestS3ScannerFallsBackToETagFingerprint(t *testing.T) {
	scanner := NewS3ScannerWithClients(
		mockS3ListClient{
			output: &s3.ListObjectsV2Output{
				Contents: []types.Object{
					{
						Key:  aws.String("file.txt"),
						ETag: aws.String(`"etag-value"`),
						Size: aws.Int64(4),
					},
				},
			},
		},
		mockS3HeadClient{output: &s3.HeadObjectOutput{}},
	)

	states, err := scanner.Scan(context.Background(), "s3://bucket")
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("len(states) = %d, want 1", len(states))
	}
	if states[0].Hash != "etag-value" {
		t.Fatalf("states[0].Hash = %q, want %q", states[0].Hash, "etag-value")
	}
	if states[0].HashAlgorithm != HashAlgorithmS3ETag {
		t.Fatalf("states[0].HashAlgorithm = %q, want %q", states[0].HashAlgorithm, HashAlgorithmS3ETag)
	}
}

type mockS3ListClient struct {
	output *s3.ListObjectsV2Output
	err    error
}

func (m mockS3ListClient) ListObjectsV2(_ context.Context, _ *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return m.output, m.err
}

type mockS3HeadClient struct {
	output *s3.HeadObjectOutput
	err    error
}

func (m mockS3HeadClient) HeadObject(_ context.Context, _ *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return m.output, m.err
}

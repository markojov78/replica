package storage

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"dropoutbox/internal/apiclient"

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

func TestS3ScannerReturnsBLAKE3InsteadOfObjectChecksumOrETag(t *testing.T) {
	modified := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	getClient := &mockS3ScannerGetClient{body: "content"}
	scanner := NewS3ScannerWithAllClients(
		&mockS3ListClient{
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
		getClient,
	)

	states, err := scanner.Scan(context.Background(), "s3://bucket/prefix", nil)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("len(states) = %d, want 1", len(states))
	}
	if states[0].RelativeURI != "nested/file.txt" {
		t.Fatalf("states[0].RelativeURI = %q, want %q", states[0].RelativeURI, "nested/file.txt")
	}
	wantHash, err := hashReaderBLAKE3(context.Background(), strings.NewReader("content"))
	if err != nil {
		t.Fatalf("hashReaderBLAKE3() error = %v", err)
	}
	if states[0].Hash != wantHash {
		t.Fatalf("states[0].Hash = %q, want BLAKE3 %q", states[0].Hash, wantHash)
	}
	if states[0].Hash == "checksum-value" || states[0].Hash == "etag-value" {
		t.Fatalf("states[0].Hash = %q, must not use S3 checksum or ETag", states[0].Hash)
	}
	if !states[0].Modified.Equal(modified) || !states[0].Created.Equal(modified) {
		t.Fatalf("modified/created mismatch: modified=%s created=%s want=%s", states[0].Modified, states[0].Created, modified)
	}
	if getClient.calls != 1 {
		t.Fatalf("GetObject calls = %d, want 1", getClient.calls)
	}
}

func TestS3ScannerReusesBLAKE3WhenMetadataIsUnchanged(t *testing.T) {
	modified := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	listClient := &mockS3ListClient{
		output: &s3.ListObjectsV2Output{
			Contents: []types.Object{
				{
					Key:          aws.String("file.txt"),
					ETag:         aws.String(`"etag-value"`),
					Size:         aws.Int64(7),
					LastModified: aws.Time(modified),
				},
			},
		},
	}
	getClient := &mockS3ScannerGetClient{body: "content"}
	scanner := NewS3ScannerWithAllClients(
		listClient,
		mockS3HeadClient{output: &s3.HeadObjectOutput{}},
		getClient,
	)

	first, err := scanner.Scan(context.Background(), "s3://bucket", nil)
	if err != nil {
		t.Fatalf("first Scan() error = %v", err)
	}
	second, err := scanner.Scan(context.Background(), "s3://bucket", nil)
	if err != nil {
		t.Fatalf("second Scan() error = %v", err)
	}
	if getClient.calls != 1 {
		t.Fatalf("GetObject calls = %d, want 1 for unchanged metadata", getClient.calls)
	}
	if len(first) != 1 || len(second) != 1 || first[0].Hash != second[0].Hash {
		t.Fatalf("first/second states = %+v/%+v, want same cached BLAKE3", first, second)
	}
}

func TestS3ScannerReusesOldHashWhenMetadataIsUnchanged(t *testing.T) {
	modified := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	headCalls := 0
	headClient := mockS3HeadClient{output: &s3.HeadObjectOutput{}, calls: &headCalls}
	getClient := &mockS3ScannerGetClient{body: "content"}
	scanner := NewS3ScannerWithAllClients(
		&mockS3ListClient{
			output: &s3.ListObjectsV2Output{
				Contents: []types.Object{
					{
						Key:          aws.String("file.txt"),
						ETag:         aws.String(`"etag-value"`),
						Size:         aws.Int64(7),
						LastModified: aws.Time(modified),
					},
				},
			},
		},
		headClient,
		getClient,
	)

	states, err := scanner.Scan(context.Background(), "s3://bucket", map[string]FileState{
		"file.txt": {
			RelativeURI: "file.txt",
			Size:        7,
			Hash:        "known-hash",
			Modified:    modified,
		},
	})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("len(states) = %d, want 1", len(states))
	}
	if states[0].Hash != "known-hash" {
		t.Fatalf("states[0].Hash = %q, want old hash", states[0].Hash)
	}
	if headCalls != 0 {
		t.Fatalf("HeadObject calls = %d, want 0", headCalls)
	}
	if getClient.calls != 0 {
		t.Fatalf("GetObject calls = %d, want 0", getClient.calls)
	}
}

func TestS3ScannerHashesWhenOldMetadataChanged(t *testing.T) {
	modified := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	headClient := mockS3HeadClient{output: &s3.HeadObjectOutput{}}
	getClient := &mockS3ScannerGetClient{body: "content"}
	scanner := NewS3ScannerWithAllClients(
		&mockS3ListClient{
			output: &s3.ListObjectsV2Output{
				Contents: []types.Object{
					{
						Key:          aws.String("file.txt"),
						ETag:         aws.String(`"etag-value"`),
						Size:         aws.Int64(7),
						LastModified: aws.Time(modified),
					},
				},
			},
		},
		headClient,
		getClient,
	)

	states, err := scanner.Scan(context.Background(), "s3://bucket", map[string]FileState{
		"file.txt": {
			RelativeURI: "file.txt",
			Size:        1,
			Hash:        "known-hash",
			Modified:    modified,
		},
	})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	wantHash, err := hashReaderBLAKE3(context.Background(), strings.NewReader("content"))
	if err != nil {
		t.Fatalf("hashReaderBLAKE3() error = %v", err)
	}
	if len(states) != 1 || states[0].Hash != wantHash {
		t.Fatalf("states = %+v, want rehashed content hash %q", states, wantHash)
	}
	if getClient.calls != 1 {
		t.Fatalf("GetObject calls = %d, want 1", getClient.calls)
	}
}

func TestS3ScannerRehashesChangedMetadataButReturnsSameBLAKE3ForSameBytes(t *testing.T) {
	modified := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	listClient := &mockS3ListClient{
		output: &s3.ListObjectsV2Output{
			Contents: []types.Object{
				{Key: aws.String("file.txt"), ETag: aws.String(`"etag-a"`), Size: aws.Int64(7), LastModified: aws.Time(modified)},
			},
		},
	}
	getClient := &mockS3ScannerGetClient{body: "content"}
	scanner := NewS3ScannerWithAllClients(listClient, mockS3HeadClient{output: &s3.HeadObjectOutput{}}, getClient)

	first, err := scanner.Scan(context.Background(), "s3://bucket", nil)
	if err != nil {
		t.Fatalf("first Scan() error = %v", err)
	}
	listClient.output.Contents[0].ETag = aws.String(`"etag-b"`)
	listClient.output.Contents[0].LastModified = aws.Time(modified.Add(time.Minute))
	second, err := scanner.Scan(context.Background(), "s3://bucket", nil)
	if err != nil {
		t.Fatalf("second Scan() error = %v", err)
	}

	if getClient.calls != 2 {
		t.Fatalf("GetObject calls = %d, want 2 after metadata change", getClient.calls)
	}
	if first[0].Hash != second[0].Hash {
		t.Fatalf("first/second states = %+v/%+v, want same BLAKE3", first[0], second[0])
	}
	files := []apiclient.ReplicaInventoryFile{
		{FileID: 10, RelativeURI: "file.txt", Size: second[0].Size, Hash: first[0].Hash, InventoryStatus: "active"},
	}
	if reports := replicaFileReportsForStates(files, second, false); len(reports) != 0 {
		t.Fatalf("reports = %+v, want no content-change report", reports)
	}
}

func TestS3ScannerIgnoresTemporaryWriteKeys(t *testing.T) {
	listClient := &mockS3ListClient{
		output: &s3.ListObjectsV2Output{
			Contents: []types.Object{
				{Key: aws.String("prefix/" + TemporaryWritePrefix + "root"), Size: aws.Int64(1)},
				{Key: aws.String("prefix/nested/" + TemporaryWritePrefix + "nested"), Size: aws.Int64(1)},
			},
		},
	}
	getClient := &mockS3ScannerGetClient{body: "content"}
	scanner := NewS3ScannerWithAllClients(listClient, mockS3HeadClient{output: &s3.HeadObjectOutput{}}, getClient)

	states, err := scanner.Scan(context.Background(), "s3://bucket/prefix", nil)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("states = %+v, want empty", states)
	}
	if getClient.calls != 0 {
		t.Fatalf("GetObject calls = %d, want 0", getClient.calls)
	}
}

type mockS3ListClient struct {
	output *s3.ListObjectsV2Output
	err    error
}

func (m *mockS3ListClient) ListObjectsV2(_ context.Context, _ *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return m.output, m.err
}

type mockS3HeadClient struct {
	output *s3.HeadObjectOutput
	err    error
	calls  *int
}

func (m mockS3HeadClient) HeadObject(_ context.Context, _ *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if m.calls != nil {
		(*m.calls)++
	}
	return m.output, m.err
}

type mockS3ScannerGetClient struct {
	body  string
	calls int
	err   error
}

func (m *mockS3ScannerGetClient) GetObject(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return &s3.GetObjectOutput{
		Body:          io.NopCloser(strings.NewReader(m.body)),
		ContentLength: aws.Int64(int64(len(m.body))),
	}, nil
}

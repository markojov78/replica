package storage

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dropoutbox/internal/apiclient"
	"dropoutbox/internal/config"

	"github.com/golang-jwt/jwt/v5"
)

func TestServeReplicaFileContentStreamsValidRequest(t *testing.T) {
	root := t.TempDir()
	content := []byte("file content")
	if err := os.WriteFile(filepath.Join(root, "file.txt"), content, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	runtime, token := newTransferTestRuntime(t, root)
	req := httptest.NewRequest(http.MethodGet, "/internal/replicas/1/files/10/content?version=7", nil)
	req.SetPathValue("replica_id", "1")
	req.SetPathValue("file_id", "10")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	runtime.ServeReplicaFileContent(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	if rec.Body.String() != string(content) {
		t.Fatalf("body = %q, want %q", rec.Body.String(), string(content))
	}
	if rec.Header().Get("Content-Length") != "12" {
		t.Fatalf("Content-Length = %q, want 12", rec.Header().Get("Content-Length"))
	}
}

func TestServeReplicaFileContentRejectsVersionMismatch(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("file content"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	runtime, token := newTransferTestRuntime(t, root)
	req := httptest.NewRequest(http.MethodGet, "/internal/replicas/1/files/10/content?version=8", nil)
	req.SetPathValue("replica_id", "1")
	req.SetPathValue("file_id", "10")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	runtime.ServeReplicaFileContent(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusConflict)
	}
}

func TestServeReplicaFileContentStreamsS3ReplicaThroughReader(t *testing.T) {
	runtime, token := newTransferTestRuntime(t, "s3://bucket/root")
	runtime.setLocalState(
		[]apiclient.Replica{{
			ID:     1,
			NodeID: "node-a",
			URI:    "s3://bucket/root",
		}},
		map[uint][]apiclient.ReplicaInventoryFile{
			1: {{
				FileID:         10,
				ReplicaID:      1,
				RelativeURI:    "file.txt",
				ReplicaStatus:  "synchronized",
				ReplicaVersion: 7,
			}},
		},
	)

	original := getTransferReader
	t.Cleanup(func() { getTransferReader = original })
	getTransferReader = func(_ context.Context, uri string) (Reader, error) {
		if uri != "s3://bucket/root" {
			t.Fatalf("reader uri = %q, want s3://bucket/root", uri)
		}
		return mockTransferReader{
			content: "s3 content",
			size:    int64(len("s3 content")),
		}, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/internal/replicas/1/files/10/content?version=7", nil)
	req.SetPathValue("replica_id", "1")
	req.SetPathValue("file_id", "10")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	runtime.ServeReplicaFileContent(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	if rec.Body.String() != "s3 content" {
		t.Fatalf("body = %q, want s3 content", rec.Body.String())
	}
	if rec.Header().Get("Content-Length") != "10" {
		t.Fatalf("Content-Length = %q, want 10", rec.Header().Get("Content-Length"))
	}
}

type mockTransferReader struct {
	content string
	size    int64
}

func (m mockTransferReader) Open(_ context.Context, _ string, _ string) (io.ReadCloser, int64, error) {
	return io.NopCloser(strings.NewReader(m.content)), m.size, nil
}

func newTransferTestRuntime(t *testing.T, root string) (*Runtime, string) {
	t.Helper()

	publicKey, privateKey := newTransferTestKeyPair(t)
	runtime, err := NewRuntime(config.Config{
		App: config.AppConfig{
			NodeID:            "node-a",
			NodeAddress:       "http://node-a",
			CoordinatorURL:    "http://coordinator",
			HeartbeatInterval: time.Minute,
		},
		Auth: config.AuthConfig{
			NodeSecret: "secret",
		},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}

	runtime.setTransferTokenPublicKey(publicKey)
	runtime.setLocalState(
		[]apiclient.Replica{{
			ID:     1,
			NodeID: "node-a",
			URI:    root,
		}},
		map[uint][]apiclient.ReplicaInventoryFile{
			1: {{
				FileID:         10,
				ReplicaID:      1,
				RelativeURI:    "file.txt",
				ReplicaStatus:  "synchronized",
				ReplicaVersion: 7,
			}},
		},
	)

	claims := transferTokenClaims{
		Purpose:           transferTokenPurpose,
		SourceReplicaID:   1,
		TargetReplicaID:   2,
		SourceNodeID:      "node-a",
		DestinationNodeID: "node-b",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "coordinator",
			Subject:   "node-b",
			Audience:  jwt.ClaimStrings{"node-a"},
			IssuedAt:  jwt.NewNumericDate(time.Now().UTC().Add(-time.Minute)),
			NotBefore: jwt.NewNumericDate(time.Now().UTC().Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(time.Now().UTC().Add(time.Minute)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(privateKey)
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}

	return runtime, token
}

func newTransferTestKeyPair(t *testing.T) (string, any) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey() error = %v", err)
	}
	publicKey := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicDER,
	})
	return string(publicKey), privateKey
}

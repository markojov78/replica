package apiclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"dropoutbox/internal/config"
)

func TestClientAuthenticateAndReportAvailability(t *testing.T) {
	var gotLoginAuth struct {
		NodeID string `json:"node_id"`
		Secret string `json:"secret"`
	}
	var gotAvailability struct {
		Address string `json:"address"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&gotLoginAuth); err != nil {
				t.Fatalf("Decode(login) error = %v", err)
			}
			_ = json.NewEncoder(w).Encode(NodeTokenPair{
				NodeID:                "node-a",
				AccessToken:           "access-token",
				RefreshToken:          "refresh-token",
				AccessTokenExpiresAt:  time.Now().UTC().Add(30 * time.Minute),
				RefreshTokenExpiresAt: time.Now().UTC().Add(8 * time.Hour),
			})
		case "/internal/nodes":
			if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
				t.Fatalf("Authorization = %q, want %q", got, "Bearer access-token")
			}
			if err := json.NewDecoder(r.Body).Decode(&gotAvailability); err != nil {
				t.Fatalf("Decode(report) error = %v", err)
			}
			_ = json.NewEncoder(w).Encode(AvailabilityReport{
				NodeID:   "node-a",
				Address:  gotAvailability.Address,
				LastSeen: "2026-05-21T12:00:00Z",
				Commands: []Command{},
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := New(config.Config{
		App: config.AppConfig{
			NodeID:         "node-a",
			CoordinatorURL: server.URL,
			NodeAddress:    "https://node-address:8081",
		},
		Auth: config.AuthConfig{
			NodeSecret: "node-secret",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	report, err := client.ReportAvailability(context.Background())
	if err != nil {
		t.Fatalf("ReportAvailability() error = %v", err)
	}

	if gotLoginAuth.NodeID != "node-a" {
		t.Fatalf("login.node_id = %q, want %q", gotLoginAuth.NodeID, "node-a")
	}
	if gotLoginAuth.Secret != "node-secret" {
		t.Fatalf("login.secret = %q, want %q", gotLoginAuth.Secret, "node-secret")
	}
	if gotAvailability.Address != "https://node-address:8081" {
		t.Fatalf("report.address = %q, want %q", gotAvailability.Address, "https://node-address:8081")
	}
	if report.NodeID != "node-a" {
		t.Fatalf("report.NodeID = %q, want %q", report.NodeID, "node-a")
	}
}

func TestClientUpdateCommandUsesInternalEndpoint(t *testing.T) {
	var gotBody struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(NodeTokenPair{
				NodeID:                "node-a",
				AccessToken:           "access-token",
				RefreshToken:          "refresh-token",
				AccessTokenExpiresAt:  time.Now().UTC().Add(30 * time.Minute),
				RefreshTokenExpiresAt: time.Now().UTC().Add(8 * time.Hour),
			})
		case "/internal/commands/7":
			if r.Method != http.MethodPatch {
				t.Fatalf("method = %s, want PATCH", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
				t.Fatalf("Authorization = %q, want %q", got, "Bearer access-token")
			}
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatalf("Decode(request body) error = %v", err)
			}
			responseError := "scan failed"
			_ = json.NewEncoder(w).Encode(Command{
				ID:        7,
				NodeID:    "node-a",
				Type:      "refresh_state",
				Status:    "failed",
				Payload:   json.RawMessage(`{"placeholder":true}`),
				CreatedAt: "2026-05-21T12:00:00Z",
				UpdatedAt: "2026-05-21T12:01:00Z",
				LastError: &responseError,
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := New(config.Config{
		App: config.AppConfig{
			NodeID:         "node-a",
			CoordinatorURL: server.URL,
			NodeAddress:    "https://node-address:8081",
		},
		Auth: config.AuthConfig{
			NodeSecret: "node-secret",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	lastError := "scan failed"
	command, err := client.UpdateCommand(context.Background(), 7, "failed", &lastError)
	if err != nil {
		t.Fatalf("UpdateCommand() error = %v", err)
	}
	if gotBody.Status != "failed" {
		t.Fatalf("request status = %q, want %q", gotBody.Status, "failed")
	}
	if gotBody.Error != "scan failed" {
		t.Fatalf("request error = %q, want %q", gotBody.Error, "scan failed")
	}
	if command.ID != 7 {
		t.Fatalf("command.ID = %d, want %d", command.ID, 7)
	}
	if command.Status != "failed" {
		t.Fatalf("command.Status = %q, want %q", command.Status, "failed")
	}
	if command.LastError == nil || *command.LastError != "scan failed" {
		t.Fatalf("command.LastError = %v, want scan failed", command.LastError)
	}
}

func TestClientListOwnReplicasUsesInternalReplicaEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(NodeTokenPair{
				NodeID:                "node-a",
				AccessToken:           "access-token",
				RefreshToken:          "refresh-token",
				AccessTokenExpiresAt:  time.Now().UTC().Add(30 * time.Minute),
				RefreshTokenExpiresAt: time.Now().UTC().Add(8 * time.Hour),
			})
		case "/internal/replicas":
			if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
				t.Fatalf("Authorization = %q, want %q", got, "Bearer access-token")
			}
			_ = json.NewEncoder(w).Encode([]Replica{
				{
					ID:          1,
					InventoryID: 2,
					NodeID:      "node-a",
					URI:         "/data",
					Status:      "active",
					Type:        "filesystem",
				},
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := New(config.Config{
		App: config.AppConfig{
			NodeID:         "node-a",
			CoordinatorURL: server.URL,
			NodeAddress:    "https://node-address:8081",
		},
		Auth: config.AuthConfig{
			NodeSecret: "node-secret",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	replicas, err := client.ListOwnReplicas(context.Background())
	if err != nil {
		t.Fatalf("ListOwnReplicas() error = %v", err)
	}
	if len(replicas) != 1 {
		t.Fatalf("len(replicas) = %d, want 1", len(replicas))
	}
	if replicas[0].NodeID != "node-a" {
		t.Fatalf("replicas[0].NodeID = %q, want %q", replicas[0].NodeID, "node-a")
	}
}

func TestClientListReplicaFilesRefreshesExpiredToken(t *testing.T) {
	refreshCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/refresh":
			refreshCalled = true
			_ = json.NewEncoder(w).Encode(NodeTokenPair{
				NodeID:                "node-a",
				AccessToken:           "new-access-token",
				RefreshToken:          "new-refresh-token",
				AccessTokenExpiresAt:  time.Now().UTC().Add(30 * time.Minute),
				RefreshTokenExpiresAt: time.Now().UTC().Add(8 * time.Hour),
			})
		case "/api/replicas/7/files":
			if got := r.Header.Get("Authorization"); got != "Bearer new-access-token" {
				t.Fatalf("Authorization = %q, want %q", got, "Bearer new-access-token")
			}
			if got := r.URL.Query().Get("page"); got != "2" {
				t.Fatalf("page query = %q, want %q", got, "2")
			}
			if got := r.URL.Query().Get("count"); got != "50" {
				t.Fatalf("count query = %q, want %q", got, "50")
			}
			_ = json.NewEncoder(w).Encode(ReplicaFileList{
				Items: []ReplicaFile{
					{
						ID:        1,
						FileID:    10,
						ReplicaID: 7,
						Version:   3,
						Status:    "synchronized",
					},
				},
				Page:  2,
				Count: 50,
				Total: 1,
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := New(config.Config{
		App: config.AppConfig{
			NodeID:         "node-a",
			CoordinatorURL: server.URL,
			NodeAddress:    "https://node-address:8081",
		},
		Auth: config.AuthConfig{
			NodeSecret: "node-secret",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	client.mu.Lock()
	client.accessToken = "expired-access-token"
	client.refreshToken = "refresh-token"
	client.accessTokenExpiresAt = time.Now().UTC().Add(-time.Minute)
	client.refreshTokenExpiresAt = time.Now().UTC().Add(time.Hour)
	client.mu.Unlock()

	files, err := client.ListReplicaFiles(context.Background(), 7, 2, 50)
	if err != nil {
		t.Fatalf("ListReplicaFiles() error = %v", err)
	}
	if !refreshCalled {
		t.Fatal("refresh endpoint was not called")
	}
	if len(files.Items) != 1 {
		t.Fatalf("len(files.Items) = %d, want 1", len(files.Items))
	}
	if files.Items[0].ReplicaID != 7 {
		t.Fatalf("files.Items[0].ReplicaID = %d, want %d", files.Items[0].ReplicaID, 7)
	}
}

func TestClientListReplicaInventoryFilesUsesInternalEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/auth/login":
			_ = json.NewEncoder(w).Encode(NodeTokenPair{
				NodeID:                "node-a",
				AccessToken:           "access-token",
				RefreshToken:          "refresh-token",
				AccessTokenExpiresAt:  time.Now().UTC().Add(30 * time.Minute),
				RefreshTokenExpiresAt: time.Now().UTC().Add(8 * time.Hour),
			})
		case "/internal/replica/7/files":
			if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
				t.Fatalf("Authorization = %q, want %q", got, "Bearer access-token")
			}
			_ = json.NewEncoder(w).Encode(ReplicaInventoryFileList{
				Files: []ReplicaInventoryFile{
					{
						FileID:           10,
						ReplicaID:        7,
						InventoryID:      3,
						RelativeURI:      "album/img.jpg",
						Size:             200,
						Hash:             "hash",
						InventoryStatus:  "active",
						InventoryVersion: 5,
						ReplicaStatus:    "pending",
						ReplicaVersion:   4,
						Created:          time.Now().UTC(),
						Modified:         time.Now().UTC(),
					},
				},
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := New(config.Config{
		App: config.AppConfig{
			NodeID:         "node-a",
			CoordinatorURL: server.URL,
			NodeAddress:    "https://node-address:8081",
		},
		Auth: config.AuthConfig{
			NodeSecret: "node-secret",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	files, err := client.ListReplicaInventoryFiles(context.Background(), 7)
	if err != nil {
		t.Fatalf("ListReplicaInventoryFiles() error = %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("len(files) = %d, want 1", len(files))
	}
	if files[0].InventoryVersion != 5 {
		t.Fatalf("files[0].InventoryVersion = %d, want 5", files[0].InventoryVersion)
	}
	if files[0].ReplicaVersion != 4 {
		t.Fatalf("files[0].ReplicaVersion = %d, want 4", files[0].ReplicaVersion)
	}
}

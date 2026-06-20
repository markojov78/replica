package apiclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"replica/internal/config"
)

func TestClientAuthenticateAndReportAvailability(t *testing.T) {
	var gotLoginAuth struct {
		NodeID string `json:"node_id"`
		Secret string `json:"secret"`
	}
	var gotAvailability struct {
		Address  string  `json:"address"`
		Interval float64 `json:"interval"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/node/auth/login":
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
		case "/node/nodes":
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
			NodeID:            "node-a",
			CoordinatorURL:    server.URL,
			NodeAddress:       "https://node-address:8081",
			HeartbeatInterval: 90 * time.Second,
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
	if gotAvailability.Interval != 90 {
		t.Fatalf("report.interval = %v, want 90", gotAvailability.Interval)
	}
	if report.NodeID != "node-a" {
		t.Fatalf("report.NodeID = %q, want %q", report.NodeID, "node-a")
	}
}

func TestClientListReplicaInventoryFilesSupportsStatusFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/node/auth/login":
			_ = json.NewEncoder(w).Encode(NodeTokenPair{
				NodeID:                "node-a",
				AccessToken:           "access-token",
				RefreshToken:          "refresh-token",
				AccessTokenExpiresAt:  time.Now().UTC().Add(30 * time.Minute),
				RefreshTokenExpiresAt: time.Now().UTC().Add(8 * time.Hour),
			})
		case "/node/replica/7/files":
			if got := r.URL.Query().Get("status"); got != "pending" {
				t.Fatalf("status query = %q, want pending", got)
			}
			_ = json.NewEncoder(w).Encode(ReplicaInventoryFileList{
				Files: []ReplicaInventoryFile{{FileID: 10, ReplicaID: 7, ReplicaStatus: "pending"}},
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	files, err := client.ListReplicaInventoryFiles(context.Background(), 7, "pending")
	if err != nil {
		t.Fatalf("ListReplicaInventoryFiles() error = %v", err)
	}
	if len(files) != 1 || files[0].FileID != 10 {
		t.Fatalf("files = %+v, want file_id=10", files)
	}
}

func TestClientUpdateReplicaFileStatusUsesInternalEndpoint(t *testing.T) {
	var gotBody struct {
		Status  string `json:"status"`
		Version uint   `json:"version"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/node/auth/login":
			_ = json.NewEncoder(w).Encode(NodeTokenPair{
				NodeID:                "node-a",
				AccessToken:           "access-token",
				RefreshToken:          "refresh-token",
				AccessTokenExpiresAt:  time.Now().UTC().Add(30 * time.Minute),
				RefreshTokenExpiresAt: time.Now().UTC().Add(8 * time.Hour),
			})
		case "/node/replica/7/files/10":
			if r.Method != http.MethodPatch {
				t.Fatalf("method = %s, want PATCH", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
				t.Fatalf("Authorization = %q, want Bearer access-token", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	version := uint(5)
	client := newTestClient(t, server.URL)
	if err := client.UpdateReplicaFileStatus(context.Background(), 7, 10, "synchronized", &version, nil); err != nil {
		t.Fatalf("UpdateReplicaFileStatus() error = %v", err)
	}
	if gotBody.Status != "synchronized" || gotBody.Version != 5 {
		t.Fatalf("request body = %+v, want synchronized version=5", gotBody)
	}
}

func TestClientTransferReplicaFileContentUsesSourceNodeEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transfer/replicas/3/files/10/content" {
			t.Fatalf("path = %q, want transfer endpoint", r.URL.Path)
		}
		if got := r.URL.Query().Get("version"); got != "5" {
			t.Fatalf("version query = %q, want 5", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer transfer-token" {
			t.Fatalf("Authorization = %q, want Bearer transfer-token", got)
		}
		_, _ = w.Write([]byte("content"))
	}))
	defer server.Close()

	client := newTestClient(t, "http://coordinator.invalid")
	body, err := client.TransferReplicaFileContent(context.Background(), server.URL, 3, 10, 5, "transfer-token")
	if err != nil {
		t.Fatalf("TransferReplicaFileContent() error = %v", err)
	}
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(data) != "content" {
		t.Fatalf("body = %q, want content", string(data))
	}
}

func TestClientUsesSeparateConfiguredRequestAndTransferTimeouts(t *testing.T) {
	client, err := New(config.Config{
		App: config.AppConfig{
			NodeID:              "node-a",
			CoordinatorURL:      "http://coordinator.example",
			NodeAddress:         "https://node-address:8081",
			APIRequestTimeout:   8 * time.Second,
			FileTransferTimeout: 45 * time.Minute,
		},
		Auth: config.AuthConfig{NodeSecret: "node-secret"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if client.httpClient.Timeout != 8*time.Second {
		t.Fatalf("httpClient.Timeout = %s, want %s", client.httpClient.Timeout, 8*time.Second)
	}
	if client.transferClient.Timeout != 45*time.Minute {
		t.Fatalf("transferClient.Timeout = %s, want %s", client.transferClient.Timeout, 45*time.Minute)
	}
}

func TestClientUsesDefaultRequestAndTransferTimeouts(t *testing.T) {
	client := newTestClient(t, "http://coordinator.example")
	if client.httpClient.Timeout != defaultAPIRequestTimeout {
		t.Fatalf("httpClient.Timeout = %s, want %s", client.httpClient.Timeout, defaultAPIRequestTimeout)
	}
	if client.transferClient.Timeout != defaultFileTransferTimeout {
		t.Fatalf("transferClient.Timeout = %s, want %s", client.transferClient.Timeout, defaultFileTransferTimeout)
	}
}

func newTestClient(t *testing.T, coordinatorURL string) *Client {
	t.Helper()
	client, err := New(config.Config{
		App: config.AppConfig{
			NodeID:         "node-a",
			CoordinatorURL: coordinatorURL,
			NodeAddress:    "https://node-address:8081",
		},
		Auth: config.AuthConfig{
			NodeSecret: "node-secret",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func TestClientGetConfigUsesNodeConfigEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/node/auth/login":
			_ = json.NewEncoder(w).Encode(NodeTokenPair{
				NodeID:                "node-a",
				AccessToken:           "access-token",
				RefreshToken:          "refresh-token",
				AccessTokenExpiresAt:  time.Now().UTC().Add(30 * time.Minute),
				RefreshTokenExpiresAt: time.Now().UTC().Add(8 * time.Hour),
			})
		case "/node/config":
			if r.Method != http.MethodGet {
				t.Fatalf("method = %s, want GET", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
				t.Fatalf("Authorization = %q, want %q", got, "Bearer access-token")
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"key": "sharing.video_inline_max_size_mb", "value": 50},
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	items, err := client.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("GetConfig() error = %v", err)
	}
	if len(items) != 1 || items[0].Key != "sharing.video_inline_max_size_mb" || string(items[0].Value) != "50" {
		t.Fatalf("GetConfig() = %+v, want config item", items)
	}
}

func TestClientUpdateCommandUsesInternalEndpoint(t *testing.T) {
	var gotBody struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/node/auth/login":
			_ = json.NewEncoder(w).Encode(NodeTokenPair{
				NodeID:                "node-a",
				AccessToken:           "access-token",
				RefreshToken:          "refresh-token",
				AccessTokenExpiresAt:  time.Now().UTC().Add(30 * time.Minute),
				RefreshTokenExpiresAt: time.Now().UTC().Add(8 * time.Hour),
			})
		case "/node/commands/7":
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
		case "/node/auth/login":
			_ = json.NewEncoder(w).Encode(NodeTokenPair{
				NodeID:                "node-a",
				AccessToken:           "access-token",
				RefreshToken:          "refresh-token",
				AccessTokenExpiresAt:  time.Now().UTC().Add(30 * time.Minute),
				RefreshTokenExpiresAt: time.Now().UTC().Add(8 * time.Hour),
			})
		case "/node/replicas":
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
		case "/node/auth/refresh":
			refreshCalled = true
			_ = json.NewEncoder(w).Encode(NodeTokenPair{
				NodeID:                "node-a",
				AccessToken:           "new-access-token",
				RefreshToken:          "new-refresh-token",
				AccessTokenExpiresAt:  time.Now().UTC().Add(30 * time.Minute),
				RefreshTokenExpiresAt: time.Now().UTC().Add(8 * time.Hour),
			})
		case "/api/admin/replicas/7/files":
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
		case "/node/auth/login":
			_ = json.NewEncoder(w).Encode(NodeTokenPair{
				NodeID:                "node-a",
				AccessToken:           "access-token",
				RefreshToken:          "refresh-token",
				AccessTokenExpiresAt:  time.Now().UTC().Add(30 * time.Minute),
				RefreshTokenExpiresAt: time.Now().UTC().Add(8 * time.Hour),
			})
		case "/node/replica/7/files":
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

func TestClientReportReplicaFilesUsesInternalEndpoint(t *testing.T) {
	fileID := uint(10)
	created := time.Date(2026, 5, 21, 11, 0, 0, 0, time.UTC)
	modified := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	size := int64(200)
	hash := "hash"
	newSize := int64(300)
	newHash := "new-hash"
	var gotBody struct {
		Files []ReplicaFileReport `json:"files"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/node/auth/login":
			_ = json.NewEncoder(w).Encode(NodeTokenPair{
				NodeID:                "node-a",
				AccessToken:           "access-token",
				RefreshToken:          "refresh-token",
				AccessTokenExpiresAt:  time.Now().UTC().Add(30 * time.Minute),
				RefreshTokenExpiresAt: time.Now().UTC().Add(8 * time.Hour),
			})
		case "/node/replica/7/files":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
				t.Fatalf("Authorization = %q, want %q", got, "Bearer access-token")
			}
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatalf("Decode(request body) error = %v", err)
			}
			w.WriteHeader(http.StatusNoContent)
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

	err = client.ReportReplicaFiles(context.Background(), 7, []ReplicaFileReport{
		{
			FileID:       &fileID,
			RelativeURI:  "album/img.jpg",
			FileSize:     &size,
			FileHash:     &hash,
			CreatedTime:  &created,
			ModifiedTime: &modified,
		},
		{
			RelativeURI:  "album/new.jpg",
			FileSize:     &newSize,
			FileHash:     &newHash,
			CreatedTime:  &created,
			ModifiedTime: &modified,
		},
	})
	if err != nil {
		t.Fatalf("ReportReplicaFiles() error = %v", err)
	}

	if len(gotBody.Files) != 2 {
		t.Fatalf("len(gotBody.Files) = %d, want 2", len(gotBody.Files))
	}
	if gotBody.Files[0].FileID == nil || *gotBody.Files[0].FileID != fileID {
		t.Fatalf("gotBody.Files[0].FileID = %v, want %d", gotBody.Files[0].FileID, fileID)
	}
	if gotBody.Files[0].RelativeURI != "album/img.jpg" {
		t.Fatalf("gotBody.Files[0].RelativeURI = %q, want album/img.jpg", gotBody.Files[0].RelativeURI)
	}
	if gotBody.Files[0].FileSize == nil || *gotBody.Files[0].FileSize != 200 {
		t.Fatalf("gotBody.Files[0].FileSize = %v, want 200", gotBody.Files[0].FileSize)
	}
	if gotBody.Files[0].FileHash == nil || *gotBody.Files[0].FileHash != "hash" {
		t.Fatalf("gotBody.Files[0].FileHash = %v, want hash", gotBody.Files[0].FileHash)
	}
	if gotBody.Files[0].CreatedTime == nil || !gotBody.Files[0].CreatedTime.Equal(created) {
		t.Fatalf("gotBody.Files[0].CreatedTime = %v, want %s", gotBody.Files[0].CreatedTime, created)
	}
	if gotBody.Files[0].ModifiedTime == nil || !gotBody.Files[0].ModifiedTime.Equal(modified) {
		t.Fatalf("gotBody.Files[0].ModifiedTime = %v, want %s", gotBody.Files[0].ModifiedTime, modified)
	}
	if gotBody.Files[1].FileID != nil {
		t.Fatalf("gotBody.Files[1].FileID = %v, want nil", gotBody.Files[1].FileID)
	}
}

func TestClientReportReplicaFilesRefreshesExpiredToken(t *testing.T) {
	refreshCalled := false
	created := time.Date(2026, 5, 21, 11, 0, 0, 0, time.UTC)
	modified := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/node/auth/refresh":
			refreshCalled = true
			_ = json.NewEncoder(w).Encode(NodeTokenPair{
				NodeID:                "node-a",
				AccessToken:           "new-access-token",
				RefreshToken:          "new-refresh-token",
				AccessTokenExpiresAt:  time.Now().UTC().Add(30 * time.Minute),
				RefreshTokenExpiresAt: time.Now().UTC().Add(8 * time.Hour),
			})
		case "/node/replica/7/files":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer new-access-token" {
				t.Fatalf("Authorization = %q, want %q", got, "Bearer new-access-token")
			}
			w.WriteHeader(http.StatusNoContent)
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

	size := int64(300)
	hash := "new-hash"
	err = client.ReportReplicaFiles(context.Background(), 7, []ReplicaFileReport{
		{
			RelativeURI:  "album/new.jpg",
			FileSize:     &size,
			FileHash:     &hash,
			CreatedTime:  &created,
			ModifiedTime: &modified,
		},
	})
	if err != nil {
		t.Fatalf("ReportReplicaFiles() error = %v", err)
	}
	if !refreshCalled {
		t.Fatal("refresh endpoint was not called")
	}
}

package storage

import (
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"replica/internal/apiclient"
	"replica/internal/config"
)

func TestRuntimeForcedRescanReadsUnchangedMetadataAndHiddenEdits(t *testing.T) {
	for _, downstream := range []bool{false, true} {
		name := "base"
		if downstream {
			name = "downstream"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			picture, err := os.Create(filepath.Join(root, "image.png"))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(picture, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
				t.Fatal(err)
			}
			if err := picture.Close(); err != nil {
				t.Fatal(err)
			}
			textPath := filepath.Join(root, "file.txt")
			if err := os.WriteFile(textPath, []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			states, err := NewFilesystemScanner().Scan(context.Background(), root, nil)
			if err != nil {
				t.Fatal(err)
			}
			var files []apiclient.ReplicaInventoryFile
			for i, state := range states {
				files = append(files, apiclient.ReplicaInventoryFile{FileID: uint(i + 1), ReplicaID: 7, RelativeURI: state.RelativeURI, Size: state.Size, Hash: state.Hash, Modified: state.Modified, Created: state.Created, InventoryStatus: "active", ReplicaStatus: "synchronized", InventoryVersion: 1, ReplicaVersion: 1})
			}
			info, err := os.Stat(textPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(textPath, []byte("after!"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(textPath, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
			var reports []apiclient.ReplicaFileReport
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/node/auth/login":
					_ = json.NewEncoder(w).Encode(apiclient.NodeTokenPair{AccessToken: "token", AccessTokenExpiresAt: time.Now().Add(time.Hour)})
				case "/node/replica/7/files":
					if r.Method == http.MethodGet {
						_ = json.NewEncoder(w).Encode(apiclient.ReplicaInventoryFileList{Files: files})
						return
					}
					var body struct {
						Files []apiclient.ReplicaFileReport `json:"files"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					reports = append(reports, body.Files...)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client, err := apiclient.New(config.Config{App: config.AppConfig{NodeID: "node-a", NodeAddress: "http://node-a", CoordinatorURL: server.URL}, Auth: config.AuthConfig{NodeSecret: "secret"}})
			if err != nil {
				t.Fatal(err)
			}
			replica := apiclient.Replica{ID: 7, URI: root, Status: "active", Type: "filesystem", InventoryType: "folder"}
			if downstream {
				upstream := uint(1)
				replica.UpstreamReplicaID = &upstream
			}
			runtime := &Runtime{client: client, replicas: []apiclient.Replica{replica}, replicaFiles: map[uint][]apiclient.ReplicaInventoryFile{}, watchers: map[uint]*runningReplicaWatcher{7: {cancel: func() {}}}}
			if err := runtime.scanReplica(context.Background(), apiclient.Command{Payload: json.RawMessage(`{"replica_id":7}`)}); err != nil {
				t.Fatal(err)
			}
			if len(reports) != 0 {
				t.Fatalf("ordinary scan reported unchanged size/mtime: %+v", reports)
			}
			if err := runtime.scanReplica(context.Background(), apiclient.Command{Payload: json.RawMessage(`{"replica_id":7,"force_rescan":true}`)}); err != nil {
				t.Fatal(err)
			}
			want := 2
			if downstream {
				want = 1
			}
			if len(reports) != want {
				t.Fatalf("reports = %+v, want %d", reports, want)
			}
			for _, report := range reports {
				if report.Action != "updated" {
					t.Fatalf("action = %s", report.Action)
				}
				if report.RelativeURI == "image.png" && len(report.Metadata) == 0 {
					t.Fatal("unchanged image missing metadata")
				}
				for _, file := range files {
					if report.RelativeURI == "file.txt" && file.RelativeURI == report.RelativeURI && *report.FileHash == file.Hash {
						t.Fatal("forced scan reused old hash")
					}
				}
			}
		})
	}
}

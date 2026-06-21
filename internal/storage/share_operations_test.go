package storage

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"replica/internal/apiclient"
)

func TestShareOperationsEnforceSharedWriteBehavior(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile(existing) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "pending.txt"), []byte("pending"), 0o600); err != nil {
		t.Fatalf("WriteFile(pending) error = %v", err)
	}

	linkHash := "public-link"
	runtime := newShareEndpointRuntime(t, "http://coordinator")
	runtime.setLocalState(
		[]apiclient.Replica{
			{ID: 3, InventoryID: 1, InventoryType: "folder", NodeID: "node-a", URI: root, Status: "active"},
			{ID: 4, InventoryID: 2, InventoryType: "file", NodeID: "node-a", URI: root, Status: "active"},
		},
		[]apiclient.Share{
			{
				ID:          1,
				InventoryID: 1,
				ReplicaID:   3,
				Status:      "active",
				UserPermissions: []apiclient.UserPermission{{
					UserID:      15,
					Permissions: []string{"read", "create", "update", "delete"},
				}},
				LinkHash:             &linkHash,
				AnonymousPermissions: []string{"read", "create", "update", "delete"},
			},
			{
				ID:                   2,
				InventoryID:          1,
				ReplicaID:            3,
				Status:               "active",
				LinkHash:             stringPtr("read-only-link"),
				AnonymousPermissions: []string{"read"},
			},
			{
				ID:          3,
				InventoryID: 2,
				ReplicaID:   4,
				Status:      "active",
				UserPermissions: []apiclient.UserPermission{{
					UserID:      15,
					Permissions: []string{"read", "create"},
				}},
			},
		},
		map[uint][]apiclient.ReplicaInventoryFile{
			3: {
				{FileID: 10, ReplicaID: 3, InventoryID: 1, RelativeURI: "existing.txt", InventoryStatus: "active", InventoryVersion: 4, ReplicaStatus: "synchronized", ReplicaVersion: 4},
				{FileID: 11, ReplicaID: 3, InventoryID: 1, RelativeURI: "pending.txt", InventoryStatus: "active", InventoryVersion: 5, ReplicaStatus: "pending", ReplicaVersion: 4},
			},
			4: {
				{FileID: 20, ReplicaID: 4, InventoryID: 2, RelativeURI: "existing.txt", InventoryStatus: "active", InventoryVersion: 1, ReplicaStatus: "synchronized", ReplicaVersion: 1},
			},
		},
	)

	assertShareOpStatus(t, runtime, runtime.CreateUserShareFile(t.Context(), 99, 1, "denied.txt", strings.NewReader("x"), 1), http.StatusForbidden)
	assertShareOpStatus(t, runtime, runtime.ReplaceUserShareFileContent(t.Context(), 15, 1, 10, "", strings.NewReader("x"), 1), http.StatusPreconditionRequired)
	assertShareOpStatus(t, runtime, runtime.DeleteUserShareFile(t.Context(), 15, 1, 10, "4"), http.StatusBadRequest)
	assertShareOpStatus(t, runtime, runtime.ReplaceUserShareFileContent(t.Context(), 15, 1, 10, `"3"`, strings.NewReader("x"), 1), http.StatusConflict)
	assertShareOpStatus(t, runtime, runtime.DeleteUserShareFile(t.Context(), 15, 1, 11, `"5"`), http.StatusConflict)
	assertShareOpStatus(t, runtime, runtime.CreatePublicShareFile(t.Context(), "read-only-link", "denied.txt", strings.NewReader("x"), 1), http.StatusForbidden)
	assertShareOpStatus(t, runtime, runtime.CreateUserShareFile(t.Context(), 15, 3, "file-inventory.txt", strings.NewReader("x"), 1), http.StatusConflict)

	if err := runtime.CreateUserShareFile(t.Context(), 15, 1, "uploaded.txt", strings.NewReader("uploaded"), int64(len("uploaded"))); err != nil {
		t.Fatalf("CreateUserShareFile() error = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "uploaded.txt")); err != nil || string(got) != "uploaded" {
		t.Fatalf("uploaded file = %q, err=%v, want uploaded", string(got), err)
	}

	if err := runtime.ReplacePublicShareFileContent(t.Context(), "public-link", 10, `"4"`, strings.NewReader("new"), int64(len("new"))); err != nil {
		t.Fatalf("ReplacePublicShareFileContent() error = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "existing.txt")); err != nil || string(got) != "new" {
		t.Fatalf("replaced file = %q, err=%v, want new", string(got), err)
	}

	if err := runtime.DeletePublicShareFile(t.Context(), "public-link", 10, `"4"`); err != nil {
		t.Fatalf("DeletePublicShareFile() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "existing.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file stat err = %v, want not exist", err)
	}
}

func assertShareOpStatus(t *testing.T, runtime *Runtime, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatalf("operation error = nil, want status %d", want)
	}
	if got := runtime.ShareOperationErrorStatus(err); got != want {
		t.Fatalf("operation error status = %d for %v, want %d", got, err, want)
	}
}

func stringPtr(value string) *string {
	return &value
}

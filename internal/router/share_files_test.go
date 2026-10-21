package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"replica/internal/model"
	"replica/internal/service"

	"gorm.io/gorm"
)

func TestShareRoutesManageFileIDs(t *testing.T) {
	database := openRouterTestDB(t)
	_, accessToken := createShareRouteUser(t, database, []model.Permission{
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionCreate},
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionRead},
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionUpdate},
	})
	replica := createShareRouteReplica(t, database, model.ReplicaStatusActive)
	handler := newShareRouteHandler(database)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Version", "1")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder
	}
	decodeShare := func(recorder *httptest.ResponseRecorder) service.ShareDetails {
		t.Helper()
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d; body=%s", recorder.Code, recorder.Body.String())
		}
		var share service.ShareDetails
		if err := json.Unmarshal(recorder.Body.Bytes(), &share); err != nil {
			t.Fatalf("Unmarshal(share) error = %v", err)
		}
		return share
	}
	assertFileIDs := func(share service.ShareDetails, want ...uint) {
		t.Helper()
		if share.FileIDs == nil {
			t.Fatalf("share.FileIDs = nil, want %v", want)
		}
		got := *share.FileIDs
		if len(got) != len(want) || share.FileCount != int64(len(want)) {
			t.Fatalf("file_ids = %v file_count = %d, want %v", got, share.FileCount, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("file_ids = %v, want %v", got, want)
			}
		}
	}

	cover := createShareRouteFile(t, database, replica.InventoryID, "album/cover.jpg", model.InventoryFileStatusActive)
	beach := createShareRouteFile(t, database, replica.InventoryID, "album/beach.jpg", model.InventoryFileStatusActive)
	deleted := createShareRouteFile(t, database, replica.InventoryID, "album/old.jpg", model.InventoryFileStatusDeleted)
	other := createShareRouteReplica(t, database, model.ReplicaStatusActive)
	foreign := createShareRouteFile(t, database, other.InventoryID, "album/foreign.jpg", model.InventoryFileStatusActive)
	replicaID := id(replica.ID)

	for _, ids := range []string{`[` + id(deleted.ID) + `]`, `[` + id(foreign.ID) + `]`, `[999999]`, `[0]`} {
		if recorder := request(http.MethodPost, "/api/admin/shares", `{"replica_id":`+replicaID+`,"file_ids":`+ids+`}`); recorder.Code != http.StatusBadRequest {
			t.Errorf("POST file_ids %s status = %d, want %d; body=%s", ids, recorder.Code, http.StatusBadRequest, recorder.Body.String())
		}
	}

	whole := decodeShare(request(http.MethodPost, "/api/admin/shares", `{"replica_id":`+replicaID+`,"name":"Everything"}`))
	assertFileIDs(whole)

	refreshBefore := countShareRouteRefreshCommands(t, database, replica.NodeID)
	subset := decodeShare(request(http.MethodPost, "/api/admin/shares", `{"replica_id":`+replicaID+`,"name":"Highlights","file_ids":[`+id(cover.ID)+`,`+id(cover.ID)+`]}`))
	assertFileIDs(subset, cover.ID)
	if got := countShareRouteRefreshCommands(t, database, replica.NodeID); got != refreshBefore+1 {
		t.Fatalf("refresh_state commands = %d, want %d", got, refreshBefore+1)
	}
	sharePath := "/api/admin/shares/" + id(subset.ID)

	assertFileIDs(decodeShare(request(http.MethodPatch, sharePath, `{"file_ids":[`+id(cover.ID)+`,`+id(beach.ID)+`]}`)), cover.ID, beach.ID)
	assertFileIDs(decodeShare(request(http.MethodPatch, sharePath, `{"name":"Renamed"}`)), cover.ID, beach.ID)
	assertFileIDs(decodeShare(request(http.MethodGet, sharePath, "")), cover.ID, beach.ID)

	// A file deleted while in the share can be resubmitted; it can't be added as a new file.
	if err := database.Model(&model.InventoryFile{}).Where("id = ?", beach.ID).Update("status", model.InventoryFileStatusDeleted).Error; err != nil {
		t.Fatalf("Update(beach deleted) error = %v", err)
	}
	assertFileIDs(decodeShare(request(http.MethodPatch, sharePath, `{"file_ids":[`+id(cover.ID)+`,`+id(beach.ID)+`]}`)), cover.ID, beach.ID)
	if recorder := request(http.MethodPatch, sharePath, `{"file_ids":[`+id(cover.ID)+`,`+id(deleted.ID)+`]}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("PATCH new deleted file status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}

	recorder := request(http.MethodGet, sharePath+"/files?sort=name&status=active", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET files status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	var files service.InventoryFileList
	if err := json.Unmarshal(recorder.Body.Bytes(), &files); err != nil {
		t.Fatalf("Unmarshal(files) error = %v", err)
	}
	if files.Total != 1 || len(files.Items) != 1 || files.Items[0].ID != cover.ID {
		t.Fatalf("files = %+v, want only active cover.jpg", files)
	}

	recorder = request(http.MethodGet, "/api/admin/shares", "")
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "file_ids") || !strings.Contains(recorder.Body.String(), `"file_count":2`) {
		t.Fatalf("GET shares body = %s, want file_count without file_ids", recorder.Body.String())
	}

	assertFileIDs(decodeShare(request(http.MethodPatch, sharePath, `{"file_ids":[]}`)))
}

func TestShareFileRoutesRequireSharePermissions(t *testing.T) {
	database := openRouterTestDB(t)
	_, accessToken := createShareRouteUser(t, database, []model.Permission{})
	replica := createShareRouteReplica(t, database, model.ReplicaStatusActive)
	share := &model.Share{ReplicaID: replica.ID, Name: "Highlights", Status: model.ShareStatusActive}
	if err := database.Create(share).Error; err != nil {
		t.Fatalf("Create(share) error = %v", err)
	}
	handler := newShareRouteHandler(database)
	sharePath := "/api/admin/shares/" + id(share.ID)

	for _, tt := range []struct{ method, path, body string }{
		{http.MethodGet, sharePath, ""},
		{http.MethodGet, sharePath + "/files", ""},
		{http.MethodPatch, sharePath, `{"file_ids":[]}`},
	} {
		req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Version", "1")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusForbidden {
			t.Errorf("%s %s status = %d, want %d", tt.method, tt.path, recorder.Code, http.StatusForbidden)
		}
	}
}

func createShareRouteFile(t *testing.T, database *gorm.DB, inventoryID uint, relativeURI string, status model.InventoryFileStatus) *model.InventoryFile {
	t.Helper()

	file := &model.InventoryFile{InventoryID: inventoryID, RelativeURI: relativeURI, Status: status, Version: 1, Size: 10, Hash: "hash-" + relativeURI}
	if err := database.Create(file).Error; err != nil {
		t.Fatalf("Create(inventory file) error = %v", err)
	}
	return file
}

func countShareRouteRefreshCommands(t *testing.T, database *gorm.DB, nodeID string) int64 {
	t.Helper()

	var count int64
	if err := database.Model(&model.Command{}).
		Where("node_id = ? AND type = ?", nodeID, model.NodeCommandTypeRefreshState).
		Count(&count).Error; err != nil {
		t.Fatalf("Count(refresh_state commands) error = %v", err)
	}
	return count
}

func id(value uint) string {
	return strconv.FormatUint(uint64(value), 10)
}

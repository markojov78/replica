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

func TestShareFileRoutesManageSubsetMembership(t *testing.T) {
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
	createShare := func(name string) service.ShareDetails {
		t.Helper()
		recorder := request(http.MethodPost, "/api/admin/shares", `{"replica_id":`+strconv.FormatUint(uint64(replica.ID), 10)+`,"name":"`+name+`"}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("create %s status = %d; body=%s", name, recorder.Code, recorder.Body.String())
		}
		var share service.ShareDetails
		if err := json.Unmarshal(recorder.Body.Bytes(), &share); err != nil {
			t.Fatalf("Unmarshal(share) error = %v", err)
		}
		return share
	}

	first := createShare("Highlights")
	second := createShare("Family")
	if first.FileCount != 0 || second.FileCount != 0 {
		t.Fatalf("file counts = %d/%d, want new shares to expose the whole replica", first.FileCount, second.FileCount)
	}

	cover := createShareRouteFile(t, database, replica.InventoryID, "album/cover.jpg", model.InventoryFileStatusActive)
	beach := createShareRouteFile(t, database, replica.InventoryID, "album/beach.jpg", model.InventoryFileStatusActive)
	deleted := createShareRouteFile(t, database, replica.InventoryID, "album/old.jpg", model.InventoryFileStatusDeleted)
	other := createShareRouteReplica(t, database, model.ReplicaStatusActive)
	foreign := createShareRouteFile(t, database, other.InventoryID, "album/foreign.jpg", model.InventoryFileStatusActive)

	sharePath := "/api/admin/shares/" + strconv.FormatUint(uint64(first.ID), 10)
	for _, ids := range []string{`[]`, `[` + id(deleted.ID) + `]`, `[` + id(foreign.ID) + `]`, `[999999]`} {
		if recorder := request(http.MethodPost, sharePath+"/files", `{"file_ids":`+ids+`}`); recorder.Code != http.StatusBadRequest {
			t.Errorf("POST file_ids %s status = %d, want %d; body=%s", ids, recorder.Code, http.StatusBadRequest, recorder.Body.String())
		}
	}

	refreshBefore := countShareRouteRefreshCommands(t, database, replica.NodeID)
	recorder := request(http.MethodPost, sharePath+"/files", `{"file_ids":[`+id(cover.ID)+`,`+id(beach.ID)+`,`+id(cover.ID)+`]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST files status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	var updated service.ShareDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &updated); err != nil {
		t.Fatalf("Unmarshal(updated) error = %v", err)
	}
	if updated.FileCount != 2 {
		t.Fatalf("file_count = %d, want 2", updated.FileCount)
	}
	if got := countShareRouteRefreshCommands(t, database, replica.NodeID); got != refreshBefore+1 {
		t.Fatalf("refresh_state commands = %d, want %d", got, refreshBefore+1)
	}
	if recorder := request(http.MethodPost, sharePath+"/files", `{"file_ids":[`+id(cover.ID)+`]}`); recorder.Code != http.StatusOK {
		t.Fatalf("POST existing member status = %d; body=%s", recorder.Code, recorder.Body.String())
	}

	recorder = request(http.MethodGet, sharePath+"/files?sort=name&path=beach", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET files status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	var files service.InventoryFileList
	if err := json.Unmarshal(recorder.Body.Bytes(), &files); err != nil {
		t.Fatalf("Unmarshal(files) error = %v", err)
	}
	if files.Total != 1 || len(files.Items) != 1 || files.Items[0].ID != beach.ID {
		t.Fatalf("files = %+v, want only beach.jpg", files)
	}

	recorder = request(http.MethodGet, "/api/admin/shares/"+strconv.FormatUint(uint64(second.ID), 10), "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"file_count":0`) {
		t.Fatalf("GET second share status = %d body=%s, want file_count 0", recorder.Code, recorder.Body.String())
	}

	if recorder := request(http.MethodDelete, sharePath+"/files/"+id(deleted.ID), ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("DELETE non-member status = %d, want %d; body=%s", recorder.Code, http.StatusNotFound, recorder.Body.String())
	}
	if recorder := request(http.MethodDelete, sharePath+"/files/"+id(beach.ID), ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("DELETE member status = %d, want %d; body=%s", recorder.Code, http.StatusNoContent, recorder.Body.String())
	}
	recorder = request(http.MethodGet, sharePath, "")
	if !strings.Contains(recorder.Body.String(), `"file_count":1`) {
		t.Fatalf("GET share body=%s, want file_count 1", recorder.Body.String())
	}
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
		{http.MethodPost, sharePath + "/files", `{"file_ids":[1]}`},
		{http.MethodDelete, sharePath + "/files/1", ""},
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

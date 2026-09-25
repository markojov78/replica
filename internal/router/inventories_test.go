package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"replica/internal/model"
	"replica/internal/service"
)

func TestInventoryFileLogRoute(t *testing.T) {
	database := openRouterTestDB(t)
	_, accessToken := createShareRouteUser(t, database, []model.Permission{
		{Resource: model.PermissionResourceInventories, Action: model.PermissionActionRead},
	})
	inventory := model.Inventory{Name: "Photos", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(&inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	file := model.InventoryFile{InventoryID: inventory.ID, RelativeURI: "photo.jpg", Status: model.InventoryFileStatusActive}
	if err := database.Create(&file).Error; err != nil {
		t.Fatalf("Create(file) error = %v", err)
	}
	entries := []model.FileJournal{
		{FileID: file.ID, InventoryID: inventory.ID, ReplicaID: 1, Version: 1, Action: model.FileJournalActionCreated, Timestamp: time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)},
		{FileID: file.ID, InventoryID: inventory.ID, ReplicaID: 1, Version: 2, Action: model.FileJournalActionUpdated, Timestamp: time.Date(2026, 5, 20, 8, 15, 30, 0, time.UTC)},
	}
	if err := database.Create(&entries).Error; err != nil {
		t.Fatalf("Create(entries) error = %v", err)
	}

	handler := newShareRouteHandler(database)
	path := "/api/admin/inventories/" + strconv.FormatUint(uint64(inventory.ID), 10) + "/files/" + strconv.FormatUint(uint64(file.ID), 10) + "/log?count=1&order=desc"
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var result service.FileJournalList
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("Unmarshal(result) error = %v", err)
	}
	if result.Total != 2 || result.Count != 1 || len(result.Items) != 1 || result.Items[0].Version != 2 {
		t.Fatalf("result = %+v, want latest entry and total 2", result)
	}

	req = httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d; body=%s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
	}
}

package router

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"replica/internal/model"
	"replica/internal/security"

	"gorm.io/gorm"
)

func nodeLogTestToken(t *testing.T, database *gorm.DB) string {
	t.Helper()
	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-b", Status: model.NodeStatusOffline, Secret: hashedSecret}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	pair, err := newRouterTestAuthService(database).NodeLogin("node-b", "node-secret", "", "", "", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}
	return pair.AccessToken
}

func postNodeLogs(t *testing.T, handler http.Handler, token string, lines string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	gz := gzip.NewWriter(&body)
	if _, err := gz.Write([]byte(lines)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/node/nodes/logs", &body)
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("X-API-Version", "1")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestNodeLogUploadAndHeartbeatCursor(t *testing.T) {
	handler, database := newUserLogTestHandler(t)
	token := nodeLogTestToken(t, database)

	lines := `{"id":7,"type":"share_access","timestamp":"2026-11-11T21:44:49Z","action":"list","success":false,"message":"share not found","anonymous":true,"source":"http"}` + "\n" +
		`{"id":8,"type":"share_access","timestamp":"2026-11-11T21:44:50Z","action":"bogus","source":"http"}` + "\n"
	response := postNodeLogs(t, handler, token, lines)
	if response.Code != http.StatusOK {
		t.Fatalf("upload = %d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		LastID   uint64 `json:"last_id"`
		Rejected []struct {
			ID    uint64 `json:"id"`
			Error string `json:"error"`
		} `json:"rejected"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.LastID != 8 || len(result.Rejected) != 1 || result.Rejected[0].ID != 8 {
		t.Fatalf("result = %+v", result)
	}
	var row model.ShareAccessLog
	if err := database.First(&row, "node_id = ? AND id = ?", "node-b", 7).Error; err != nil {
		t.Fatalf("stored row: %v", err)
	}
	if row.Success || row.Message == nil || *row.Message != "share not found" {
		t.Fatalf("row = %+v", row)
	}

	if response := postNodeLogs(t, handler, "", lines); response.Code != http.StatusUnauthorized {
		t.Fatalf("upload without token = %d, want 401", response.Code)
	}

	heartbeat := httptest.NewRequest(http.MethodPost, "/node/nodes", strings.NewReader(`{"address":"http://node-b","interval":60}`))
	heartbeat.Header.Set("Content-Type", "application/json")
	heartbeat.Header.Set("X-API-Version", "1")
	heartbeat.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, heartbeat)
	if recorder.Code != http.StatusOK {
		t.Fatalf("heartbeat = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var report struct {
		Logs *struct {
			LastID uint64 `json:"last_id"`
		} `json:"logs"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode heartbeat: %v", err)
	}
	if report.Logs == nil || report.Logs.LastID != 8 {
		t.Fatalf("heartbeat logs = %+v, want last_id 8", report.Logs)
	}
}

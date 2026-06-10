package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"replica/internal/buildinfo"
	"replica/internal/config"
	"replica/internal/db"
	"replica/internal/model"
	"replica/internal/repository"
	"replica/internal/seed"
	"replica/internal/service"
)

func TestAdminUIRequiresLoginAndManagesInventory(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "admin-ui.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}
	if err := seed.Run(database, config.SeedConfig{AdminName: "admin", AdminPassword: "secret"}); err != nil {
		t.Fatalf("seed.Run() error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-a", Status: model.NodeStatusOffline, Secret: "ignored"}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-b", Status: model.NodeStatusOffline, Secret: "ignored"}).Error; err != nil {
		t.Fatalf("Create(node-b) error = %v", err)
	}

	nodeRepo := repository.NewNodeRepository(database)
	commandRepo := repository.NewNodeCommandRepository(database)
	inventoryRepo := repository.NewInventoryRepository(database)
	nodeService := service.NewNodeService(nodeRepo, commandRepo)
	settingService := service.NewSettingService(repository.NewSettingRepository(database))
	handler := New(
		config.Config{App: config.AppConfig{Coordinator: true}},
		buildinfo.Info{Version: "test"},
		service.NewAuthService(
			repository.NewUserRepository(database),
			repository.NewUserTokenRepository(database),
			nodeRepo,
			repository.NewNodeTokenRepository(database),
			"test-secret",
			30*time.Minute,
			8*time.Hour,
		),
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		nodeService,
		service.NewInventoryService(inventoryRepo, nodeService),
		service.NewReplicaService(repository.NewReplicaRepository(database), inventoryRepo, nodeService, settingService),
	)

	response := adminRequest(t, handler, http.MethodGet, "/admin/nodes", nil, "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "data-login-form") {
		t.Fatalf("protected response = %d body=%q, want login page", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/admin/static/admin.js", nil, "")
	for _, required := range []string{
		"localStorage",
		"access_token_expires_at",
		"refresh_token_expires_at",
		"/api/auth/login",
		"/api/auth/refresh",
		"/api/auth/logout",
		"/api/auth/me",
	} {
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), required) {
			t.Fatalf("admin.js response = %d, missing %q", response.Code, required)
		}
	}

	loginBody, err := json.Marshal(map[string]string{"username": "admin", "password": "secret"})
	if err != nil {
		t.Fatalf("json.Marshal(login) error = %v", err)
	}
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(loginBody))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginRequest.Header.Set("X-API-Version", "1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, loginRequest)
	if response.Code != http.StatusOK {
		t.Fatalf("login response = %d body=%q", response.Code, response.Body.String())
	}
	var pair struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&pair); err != nil {
		t.Fatalf("decode login response error = %v", err)
	}
	accessToken := pair.AccessToken

	response = adminRequest(t, handler, http.MethodGet, "/admin/inventories", nil, accessToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Inventories") {
		t.Fatalf("inventories response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/admin/inventories", url.Values{
		"name":    {"Documents"},
		"type":    {"folder"},
		"node_id": {"node-a"},
		"uri":     {"/srv/documents"},
	}, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/admin/inventories/1" {
		t.Fatalf("create inventory response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}

	now := time.Now().UTC()
	files := make([]model.InventoryFile, 0, 21)
	for i := 1; i <= 21; i++ {
		files = append(files, model.InventoryFile{
			InventoryID: 1,
			RelativeURI: "file-" + strconv.Itoa(i) + ".txt",
			Status:      model.InventoryFileStatusActive,
			Size:        int64(i * 1024),
			Hash:        "hash-" + strconv.Itoa(i),
			Version:     uint(i),
			Created:     now,
			Modified:    now,
		})
	}
	if err := database.Create(&files).Error; err != nil {
		t.Fatalf("Create(files) error = %v", err)
	}

	response = adminRequest(t, handler, http.MethodGet, "/admin/inventories/1", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "Documents") ||
		!strings.Contains(response.Body.String(), "Replicas") ||
		!strings.Contains(response.Body.String(), "Inventory files") ||
		!strings.Contains(response.Body.String(), "file-1.txt") ||
		!strings.Contains(response.Body.String(), "20 of 21 files, page 1 of 2") ||
		!strings.Contains(response.Body.String(), "/admin/inventories/1?page=2&count=20") ||
		!strings.Contains(response.Body.String(), "Files per page") {
		t.Fatalf("inventory detail response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/admin/inventories/1?page=2&count=20", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "file-21.txt") ||
		!strings.Contains(response.Body.String(), "1 of 21 files, page 2 of 2") ||
		!strings.Contains(response.Body.String(), "/admin/inventories/1?page=1&count=20") {
		t.Fatalf("inventory files page 2 response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/admin/inventories/1?page=2&count=10", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "10 of 21 files, page 2 of 3") ||
		!strings.Contains(response.Body.String(), "/admin/inventories/1?page=1&count=10") ||
		!strings.Contains(response.Body.String(), "/admin/inventories/1?page=3&count=10") {
		t.Fatalf("inventory files custom page size response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/admin/inventories/1/replicas", url.Values{
		"node_id":             {"node-b"},
		"uri":                 {"/backup/documents"},
		"type":                {"filesystem"},
		"upstream_replica_id": {"1"},
	}, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/admin/inventories/1" {
		t.Fatalf("create replica response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/admin/inventories/1/replicas/2", url.Values{
		"type":                {"filesystem"},
		"status":              {"active"},
		"upstream_replica_id": {""},
	}, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/admin/inventories/1" {
		t.Fatalf("update replica response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	var updatedReplica model.Replica
	if err := database.First(&updatedReplica, 2).Error; err != nil {
		t.Fatalf("First(replica) error = %v", err)
	}
	if updatedReplica.UpstreamReplicaID != nil {
		t.Fatalf("updatedReplica.UpstreamReplicaID = %v, want nil", updatedReplica.UpstreamReplicaID)
	}

	response = adminRequest(t, handler, http.MethodPost, "/admin/inventories/999/delete", nil, accessToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "inventory not found") {
		t.Fatalf("delete missing inventory response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/admin/inventories/1/delete", nil, accessToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "inventory has active replicas") {
		t.Fatalf("delete active inventory response = %d body=%q", response.Code, response.Body.String())
	}

	for _, replicaID := range []string{"2", "1"} {
		response = adminRequest(t, handler, http.MethodPost, "/admin/inventories/1/replicas/"+replicaID+"/delete", nil, accessToken)
		if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/admin/inventories/1" {
			t.Fatalf("delete replica %s response = %d location=%q body=%q", replicaID, response.Code, response.Header().Get("Location"), response.Body.String())
		}
	}

	response = adminRequest(t, handler, http.MethodPost, "/admin/inventories/1/delete", nil, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/admin/inventories" {
		t.Fatalf("delete inventory response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	var deletedInventory model.Inventory
	if err := database.First(&deletedInventory, 1).Error; err != nil {
		t.Fatalf("First(deleted inventory) error = %v", err)
	}
	if deletedInventory.Status != model.InventoryStatusDeleted {
		t.Fatalf("deletedInventory.Status = %q, want %q", deletedInventory.Status, model.InventoryStatusDeleted)
	}

	response = adminRequest(t, handler, http.MethodGet, "/admin/nodes", nil, accessToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "node-a") {
		t.Fatalf("nodes response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/admin/nodes", nil, "invalid")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token response = %d body=%q, want 401", response.Code, response.Body.String())
	}
}

func adminRequest(t *testing.T, handler http.Handler, method, path string, form url.Values, accessToken string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if accessToken != "" {
		request.Header.Set("Authorization", "Bearer "+accessToken)
	}
	request.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

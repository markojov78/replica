package router

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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

	response := adminRequest(t, handler, http.MethodGet, "/admin/nodes", nil, nil)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/admin/login" {
		t.Fatalf("protected response = %d location=%q, want 303 /admin/login", response.Code, response.Header().Get("Location"))
	}

	response = adminRequest(t, handler, http.MethodPost, "/admin/login", url.Values{
		"username": {"admin"},
		"password": {"secret"},
	}, nil)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/admin" {
		t.Fatalf("login response = %d location=%q", response.Code, response.Header().Get("Location"))
	}
	cookies := response.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login response has no session cookie")
	}

	response = adminRequest(t, handler, http.MethodGet, "/admin/inventories", nil, cookies)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Inventories") {
		t.Fatalf("inventories response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/admin/inventories", url.Values{
		"name":    {"Documents"},
		"type":    {"folder"},
		"node_id": {"node-a"},
		"uri":     {"/srv/documents"},
	}, cookies)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/admin/inventories/1" {
		t.Fatalf("create inventory response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/admin/inventories/1", nil, cookies)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Documents") || !strings.Contains(response.Body.String(), "Replicas") {
		t.Fatalf("inventory detail response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/admin/inventories/1/replicas", url.Values{
		"node_id":             {"node-b"},
		"uri":                 {"/backup/documents"},
		"type":                {"filesystem"},
		"upstream_replica_id": {"1"},
	}, cookies)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/admin/inventories/1" {
		t.Fatalf("create replica response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/admin/inventories/1/replicas/2", url.Values{
		"type":                {"filesystem"},
		"status":              {"active"},
		"upstream_replica_id": {""},
	}, cookies)
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

	response = adminRequest(t, handler, http.MethodGet, "/admin/nodes", nil, cookies)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "node-a") {
		t.Fatalf("nodes response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/admin/logout", nil, cookies)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/admin/login" {
		t.Fatalf("logout response = %d location=%q", response.Code, response.Header().Get("Location"))
	}
}

func adminRequest(t *testing.T, handler http.Handler, method, path string, form url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader
	if form == nil {
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(form.Encode())
	}
	request := httptest.NewRequest(method, path, body)
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

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
	"replica/internal/security"
	"replica/internal/seed"
	"replica/internal/service"
	"replica/internal/ui/uiauth"

	"gorm.io/gorm"
)

func newUserLogTestHandler(t *testing.T) (http.Handler, *gorm.DB) {
	t.Helper()
	database, err := db.Open(config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "user-logs.db")})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}
	if err := seed.Run(database, config.SeedConfig{AdminName: "admin", AdminPassword: "secret"}); err != nil {
		t.Fatalf("seed.Run() error = %v", err)
	}

	logs := service.NewUserLogService(repository.NewUserLogRepository(database), "node-a")
	nodeRepo := repository.NewNodeRepository(database)
	commandRepo := repository.NewNodeCommandRepository(database)
	inventoryRepo := repository.NewInventoryRepository(database)
	nodeService := service.NewNodeService(nodeRepo, commandRepo).WithLogs(service.NewNodeLogService(repository.NewNodeLogRepository(database))).
		WithChangeLogs(service.NewChangeLogService(repository.NewChangeLogRepository(database)))
	settingService := service.NewSettingService(repository.NewSettingRepository(database))
	handler := New(
		config.Config{
			App:  config.AppConfig{Coordinator: true, NodeID: "node-a", HeartbeatInterval: 60 * time.Second},
			HTTP: config.HTTPConfig{Address: ":8080"},
		},
		buildinfo.Info{Version: "test"},
		service.NewAuthService(
			repository.NewUserRepository(database),
			repository.NewUserTokenRepository(database),
			nodeRepo,
			repository.NewNodeTokenRepository(database),
			"test-secret",
			30*time.Minute,
			8*time.Hour,
		).WithUserLogs(logs),
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database), repository.NewUserTokenRepository(database)).WithUserLogs(logs),
		service.NewRoleService(repository.NewRoleRepository(database)).WithUserLogs(logs),
		nodeService,
		service.NewInventoryService(inventoryRepo, nodeService),
		service.NewReplicaService(repository.NewReplicaRepository(database), inventoryRepo, nodeService, settingService),
		service.NewShareService(repository.NewShareRepository(database), nil, func() config.SharingConfig { return config.SharingConfig{} }).
			WithShareLogs(service.NewShareLogService(repository.NewShareLogRepository(database), "node-a")),
		nil,
	)
	return handler, database
}

func userLogJSONRequest(t *testing.T, handler http.Handler, method, path, remoteAddr string, body any, accessToken string) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	request.RemoteAddr = remoteAddr
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-API-Version", "1")
	request.Header.Set("X-Forwarded-For", "10.9.9.9")
	if accessToken != "" {
		request.Header.Set("Authorization", "Bearer "+accessToken)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func userLogLogin(t *testing.T, handler http.Handler, username, remoteAddr string) (string, uint) {
	t.Helper()
	response := userLogJSONRequest(t, handler, http.MethodPost, "/api/admin/auth/login", remoteAddr, map[string]string{"username": username, "password": "secret"}, "")
	if response.Code != http.StatusOK {
		t.Fatalf("login %s = %d body=%s", username, response.Code, response.Body.String())
	}
	var pair struct {
		UserID      uint   `json:"user_id"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &pair); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	return pair.AccessToken, pair.UserID
}

func latestUserLog(t *testing.T, database *gorm.DB) model.UserLog {
	t.Helper()
	var row model.UserLog
	if err := database.Order("id desc").First(&row).Error; err != nil {
		t.Fatalf("First(user_logs) error = %v", err)
	}
	return row
}

func TestUserLogsAdminAPI(t *testing.T) {
	handler, database := newUserLogTestHandler(t)

	hashed, err := security.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	viewer := &model.User{Username: "viewer", Status: model.UserStatusActive, Password: hashed}
	if err := database.Create(viewer).Error; err != nil {
		t.Fatalf("Create(viewer) error = %v", err)
	}

	viewerToken, _ := userLogLogin(t, handler, "viewer", "203.0.113.9:5555")
	row := latestUserLog(t, database)
	if row.Action != model.UserLogActionLogin || !row.Success || row.SourceAddress == nil || *row.SourceAddress != "203.0.113.9" || row.NodeID != "node-a" {
		t.Fatalf("login row = %+v, want successful login from 203.0.113.9 (forwarded header ignored)", row)
	}

	adminToken, adminID := userLogLogin(t, handler, "admin", "198.51.100.4:4000")

	// A caller without users:update gets 403 and a failed update row.
	name := "renamed"
	response := userLogJSONRequest(t, handler, http.MethodPatch, "/api/admin/users/"+strconv.FormatUint(uint64(adminID), 10), "203.0.113.9:5555", map[string]any{"name": name}, viewerToken)
	if response.Code != http.StatusForbidden {
		t.Fatalf("PATCH as viewer = %d, want 403", response.Code)
	}
	row = latestUserLog(t, database)
	if row.Action != model.UserLogActionUpdate || row.Success || row.ActionUserID == nil || *row.ActionUserID != viewer.ID ||
		row.UserID == nil || *row.UserID != adminID || row.Message == nil || *row.Message != "permission denied" {
		t.Fatalf("denied row = %+v", row)
	}

	// Unauthenticated mutations are not logged.
	var before int64
	database.Model(&model.UserLog{}).Count(&before)
	response = userLogJSONRequest(t, handler, http.MethodDelete, "/api/admin/users/"+strconv.FormatUint(uint64(viewer.ID), 10), "203.0.113.9:5555", nil, "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("DELETE without token = %d, want 401", response.Code)
	}
	var after int64
	database.Model(&model.UserLog{}).Count(&after)
	if after != before {
		t.Fatalf("401 request wrote %d user log rows", after-before)
	}

	// An admin update records the actor and the admin's address.
	response = userLogJSONRequest(t, handler, http.MethodPatch, "/api/admin/users/"+strconv.FormatUint(uint64(viewer.ID), 10), "198.51.100.4:4000", map[string]any{"name": "Viewer"}, adminToken)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH as admin = %d body=%s", response.Code, response.Body.String())
	}
	row = latestUserLog(t, database)
	if row.Action != model.UserLogActionUpdate || !row.Success || row.ActionUserID == nil || *row.ActionUserID != adminID ||
		row.SourceAddress == nil || *row.SourceAddress != "198.51.100.4" || row.Message == nil || !strings.Contains(*row.Message, "name:  -> Viewer") {
		t.Fatalf("update row = %+v message=%q", row, func() string {
			if row.Message == nil {
				return ""
			}
			return *row.Message
		}())
	}
}

func TestUserLogsDashboardKeepsClientAddress(t *testing.T) {
	handler, database := newUserLogTestHandler(t)
	adminToken, adminID := userLogLogin(t, handler, "admin", "198.51.100.4:4000")

	form := url.Values{"username": {"admin"}, "name": {"Administrator"}, "status": {"active"}}
	request := httptest.NewRequest(http.MethodPost, "/dashboard/users/"+strconv.FormatUint(uint64(adminID), 10), strings.NewReader(form.Encode()))
	request.RemoteAddr = "192.0.2.77:9999"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: uiauth.AccessCookieName, Value: adminToken, Path: "/"})
	request.AddCookie(&http.Cookie{Name: "replica_admin_csrf", Value: "test-csrf", Path: "/dashboard"})
	request.Header.Set("X-CSRF-Token", "test-csrf")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("dashboard update = %d body=%s", response.Code, response.Body.String())
	}

	var rows []model.UserLog
	if err := database.Where("action = ? AND success = ?", model.UserLogActionUpdate, true).Find(&rows).Error; err != nil {
		t.Fatalf("Find(user_logs) error = %v", err)
	}
	found := false
	for _, row := range rows {
		if row.UserID != nil && *row.UserID == adminID && row.Message != nil && strings.Contains(*row.Message, "name:  -> Administrator") {
			found = true
			if row.SourceAddress == nil || *row.SourceAddress != "192.0.2.77" {
				t.Fatalf("dashboard update source_address = %v, want 192.0.2.77", row.SourceAddress)
			}
		}
	}
	if !found {
		t.Fatalf("no dashboard update row found: %+v", rows)
	}
}

func TestUserLogsNodeUserAuthUsesNodeAttribution(t *testing.T) {
	handler, database := newUserLogTestHandler(t)

	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-b", Status: model.NodeStatusOffline, Secret: hashedSecret}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	nodePair, err := newRouterTestAuthService(database).NodeLogin("node-b", "node-secret", "", "", "", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}

	assertRow := func(action model.UserLogAction) {
		t.Helper()
		row := latestUserLog(t, database)
		if row.Action != action || !row.Success || row.NodeID != "node-b" || row.SourceAddress == nil || *row.SourceAddress != "203.0.113.5" {
			t.Fatalf("%s row = %+v, want node-b and client address 203.0.113.5", action, row)
		}
	}

	response := userLogJSONRequest(t, handler, http.MethodPost, "/node/users/auth/login", "10.0.0.2:4000",
		map[string]string{"username": "admin", "password": "secret", "source_address": "203.0.113.5"}, nodePair.AccessToken)
	if response.Code != http.StatusOK {
		t.Fatalf("node user login = %d body=%s", response.Code, response.Body.String())
	}
	assertRow(model.UserLogActionLogin)
	var pair struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &pair); err != nil {
		t.Fatalf("decode login: %v", err)
	}

	response = userLogJSONRequest(t, handler, http.MethodPost, "/node/users/auth/refresh", "10.0.0.2:4000",
		map[string]string{"refresh_token": pair.RefreshToken, "source_address": "203.0.113.5"}, nodePair.AccessToken)
	if response.Code != http.StatusOK {
		t.Fatalf("node user refresh = %d body=%s", response.Code, response.Body.String())
	}
	assertRow(model.UserLogActionTokenRefresh)
	if err := json.Unmarshal(response.Body.Bytes(), &pair); err != nil {
		t.Fatalf("decode refresh: %v", err)
	}

	response = userLogJSONRequest(t, handler, http.MethodPost, "/node/users/auth/logout", "10.0.0.2:4000",
		map[string]string{"access_token": pair.AccessToken, "source_address": "203.0.113.5"}, nodePair.AccessToken)
	if response.Code != http.StatusNoContent {
		t.Fatalf("node user logout = %d body=%s", response.Code, response.Body.String())
	}
	assertRow(model.UserLogActionLogout)

	// Without a valid node token the request is rejected before any user auth and nothing is logged.
	var before int64
	database.Model(&model.UserLog{}).Count(&before)
	response = userLogJSONRequest(t, handler, http.MethodPost, "/node/users/auth/login", "10.0.0.2:4000",
		map[string]string{"username": "admin", "password": "wrong", "source_address": "203.0.113.5"}, "not-a-node-token")
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "missing authenticated node") {
		t.Fatalf("invalid node token = %d body=%s, want 401 missing authenticated node", response.Code, response.Body.String())
	}
	var after int64
	database.Model(&model.UserLog{}).Count(&after)
	if after != before {
		t.Fatalf("rejected node request wrote %d user log rows", after-before)
	}
}

func TestShareLogsAdminAPI(t *testing.T) {
	handler, database := newUserLogTestHandler(t)

	inventory := &model.Inventory{Name: "photos", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-b", Status: model.NodeStatusOnline, Address: "https://node-b.example"}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	replica := &model.Replica{InventoryID: inventory.ID, NodeID: "node-b", URI: "/data/a", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem}
	if err := database.Create(replica).Error; err != nil {
		t.Fatalf("Create(replica) error = %v", err)
	}
	hashed, err := security.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	viewer := &model.User{Username: "viewer", Status: model.UserStatusActive, Password: hashed}
	if err := database.Create(viewer).Error; err != nil {
		t.Fatalf("Create(viewer) error = %v", err)
	}
	adminToken, adminID := userLogLogin(t, handler, "admin", "198.51.100.4:4000")
	viewerToken, _ := userLogLogin(t, handler, "viewer", "203.0.113.9:5555")

	response := userLogJSONRequest(t, handler, http.MethodPost, "/api/admin/shares", "198.51.100.4:4000", map[string]any{"replica_id": replica.ID, "name": "holiday"}, adminToken)
	if response.Code != http.StatusOK {
		t.Fatalf("POST /shares = %d body=%s", response.Code, response.Body.String())
	}
	var share struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &share); err != nil {
		t.Fatalf("decode share: %v", err)
	}
	var row model.ShareLog
	if err := database.Order("id desc").First(&row).Error; err != nil {
		t.Fatalf("First(share_logs) error = %v", err)
	}
	if row.Action != model.ShareLogActionCreate || !row.Success || row.ShareID == nil || *row.ShareID != share.ID ||
		row.ActionUserID == nil || *row.ActionUserID != adminID || row.SourceAddress == nil || *row.SourceAddress != "198.51.100.4" || row.NodeID != "node-a" {
		t.Fatalf("create row = %+v", row)
	}

	response = userLogJSONRequest(t, handler, http.MethodPatch, "/api/admin/shares/"+strconv.FormatUint(uint64(share.ID), 10), "203.0.113.9:5555", map[string]any{"name": "renamed"}, viewerToken)
	if response.Code != http.StatusForbidden {
		t.Fatalf("PATCH as viewer = %d body=%s, want 403", response.Code, response.Body.String())
	}
	row = model.ShareLog{}
	if err := database.Order("id desc").First(&row).Error; err != nil {
		t.Fatalf("First(share_logs) error = %v", err)
	}
	if row.Action != model.ShareLogActionUpdate || row.Success || row.ShareID == nil || *row.ShareID != share.ID ||
		row.ActionUserID == nil || *row.ActionUserID != viewer.ID || row.Message == nil || *row.Message != "permission denied" ||
		row.SourceAddress == nil || *row.SourceAddress != "203.0.113.9" {
		t.Fatalf("denied row = %+v", row)
	}
}

func TestNodeLogsAdminAPI(t *testing.T) {
	handler, database := newUserLogTestHandler(t)
	if err := database.Create(&model.Node{ID: "node-c", Status: model.NodeStatusOffline, Secret: "x"}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	hashed, err := security.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := database.Create(&model.User{Username: "viewer", Status: model.UserStatusActive, Password: hashed}).Error; err != nil {
		t.Fatalf("Create(viewer) error = %v", err)
	}
	adminToken, adminID := userLogLogin(t, handler, "admin", "198.51.100.4:4000")
	viewerToken, _ := userLogLogin(t, handler, "viewer", "203.0.113.9:5555")

	response := userLogJSONRequest(t, handler, http.MethodPatch, "/api/admin/nodes/node-c", "203.0.113.9:5555", map[string]any{"address": "http://node-c:1"}, viewerToken)
	if response.Code != http.StatusForbidden {
		t.Fatalf("PATCH as viewer = %d, want 403", response.Code)
	}
	var count int64
	database.Model(&model.NodeLog{}).Count(&count)
	if count != 0 {
		t.Fatalf("403 wrote %d node_logs rows", count)
	}

	response = userLogJSONRequest(t, handler, http.MethodPatch, "/api/admin/nodes/node-c", "198.51.100.4:4000", map[string]any{"address": "http://node-c:2"}, adminToken)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH as admin = %d body=%s", response.Code, response.Body.String())
	}
	var row model.NodeLog
	if err := database.First(&row).Error; err != nil {
		t.Fatalf("First(node_logs) error = %v", err)
	}
	if row.Action != model.NodeLogActionUpdate || row.NodeID != "node-c" || row.ActionUserID == nil || *row.ActionUserID != adminID ||
		row.SourceAddress == nil || *row.SourceAddress != "198.51.100.4" || row.Message == nil || *row.Message != "address:  -> http://node-c:2" {
		t.Fatalf("row = %+v", row)
	}
}

func TestReplicaUpdateAuthorizedByInventoryPermission(t *testing.T) {
	handler, database := newUserLogTestHandler(t)
	inventory := &model.Inventory{Name: "photos", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-b", Status: model.NodeStatusOffline, Secret: "x"}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	replica := &model.Replica{InventoryID: inventory.ID, NodeID: "node-b", URI: "/data/a", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem}
	if err := database.Create(replica).Error; err != nil {
		t.Fatalf("Create(replica) error = %v", err)
	}
	hashed, err := security.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	editor := &model.User{Username: "editor", Status: model.UserStatusActive, Password: hashed}
	outsider := &model.User{Username: "outsider", Status: model.UserStatusActive, Password: hashed}
	for _, user := range []*model.User{editor, outsider} {
		if err := database.Create(user).Error; err != nil {
			t.Fatalf("Create(user) error = %v", err)
		}
	}
	inventoryUser := &model.InventoryUser{UserID: editor.ID, InventoryID: inventory.ID}
	if err := database.Create(inventoryUser).Error; err != nil {
		t.Fatalf("Create(inventory user) error = %v", err)
	}
	if err := database.Create(&model.InventoryPermission{InventoryUserID: inventoryUser.ID, Permission: string(model.PermissionActionUpdate)}).Error; err != nil {
		t.Fatalf("Create(inventory permission) error = %v", err)
	}
	editorToken, _ := userLogLogin(t, handler, "editor", "203.0.113.9:5555")
	outsiderToken, _ := userLogLogin(t, handler, "outsider", "203.0.113.9:5555")

	path := "/api/admin/replicas/" + strconv.FormatUint(uint64(replica.ID), 10)
	if response := userLogJSONRequest(t, handler, http.MethodPatch, path, "203.0.113.9:5555", map[string]any{"follow_symlinks": true}, outsiderToken); response.Code != http.StatusForbidden {
		t.Fatalf("PATCH without permission = %d body=%s, want 403", response.Code, response.Body.String())
	}
	if response := userLogJSONRequest(t, handler, http.MethodPatch, path, "203.0.113.9:5555", map[string]any{"follow_symlinks": true}, editorToken); response.Code != http.StatusOK {
		t.Fatalf("PATCH with inventory update permission = %d body=%s, want 200", response.Code, response.Body.String())
	}
}

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"replica/internal/buildinfo"
	"replica/internal/config"
	"replica/internal/model"
	"replica/internal/repository"
	"replica/internal/security"
	"replica/internal/service"

	"gorm.io/gorm"
)

func TestAdminConfigRoutesRequireSettingsPermissionsAndUpdateConfig(t *testing.T) {
	database := openRouterTestDB(t)
	if err := database.Create(&model.Node{ID: "node-a", Status: model.NodeStatusOnline, Secret: "secret"}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	token := createConfigUserToken(t, database, []model.Permission{
		{Resource: model.PermissionResourceSettings, Action: model.PermissionActionRead},
		{Resource: model.PermissionResourceSettings, Action: model.PermissionActionUpdate},
	})
	handler := newConfigRouteHandler(database)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/config", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusOK)
	}
	var listed service.ConfigList
	if err := json.Unmarshal(recorder.Body.Bytes(), &listed); err != nil {
		t.Fatalf("Unmarshal(GET) error = %v", err)
	}
	if len(listed.Items) != 5 {
		t.Fatalf("items = %d, want 5", len(listed.Items))
	}
	if got := configItemNumberSlice(t, listed.Items, config.SettingSharingThumbnailSizes); len(got) != 3 || got[0] != 128 || got[1] != 256 || got[2] != 512 {
		t.Fatalf("GET thumbnail sizes = %+v, want [128 256 512]", got)
	}

	req = httptest.NewRequest(http.MethodPatch, "/api/admin/config", strings.NewReader(`{"items":[{"key":"sharing.video_inline_max_size_mb","value":50}]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusOK)
	}
	var patched service.ConfigList
	if err := json.Unmarshal(recorder.Body.Bytes(), &patched); err != nil {
		t.Fatalf("Unmarshal(PATCH) error = %v", err)
	}
	if got := configItemNumber(t, patched.Items, config.SettingSharingVideoInlineMaxSizeMB); got != 50 {
		t.Fatalf("patched video_inline_max_size_mb = %d, want 50", got)
	}

	var command model.Command
	if err := database.First(&command, "node_id = ? AND type = ?", "node-a", model.NodeCommandTypeRefreshConfig).Error; err != nil {
		t.Fatalf("First(refresh_config command) error = %v", err)
	}
	if string(command.Payload) != "{}" {
		t.Fatalf("command.Payload = %s, want {}", command.Payload)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/admin/config/sharing.video_inline_max_size_mb", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("DELETE key status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusNoContent)
	}
}

func TestAdminConfigPatchRejectsInvalidValue(t *testing.T) {
	database := openRouterTestDB(t)
	token := createConfigUserToken(t, database, []model.Permission{
		{Resource: model.PermissionResourceSettings, Action: model.PermissionActionUpdate},
	})
	handler := newConfigRouteHandler(database)

	req := httptest.NewRequest(http.MethodPatch, "/api/admin/config", strings.NewReader(`{"items":[{"key":"sharing.video_inline_max_size_mb","value":"50"}]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusBadRequest)
	}
}

func TestAdminConfigRoutesRejectMissingPermission(t *testing.T) {
	database := openRouterTestDB(t)
	token := createConfigUserToken(t, database, nil)
	handler := newConfigRouteHandler(database)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/config", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusForbidden)
	}
}

func TestNodeConfigRouteRequiresNodeTokenAndReturnsArray(t *testing.T) {
	database := openRouterTestDB(t)
	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword(node) error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-a", Status: model.NodeStatusOnline, Secret: hashedSecret}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	authService := newRouterTestAuthService(database)
	pair, err := authService.NodeLogin("node-a", "node-secret", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}
	handler := newConfigRouteHandlerWithAuth(database, authService)

	req := httptest.NewRequest(http.MethodGet, "/node/config", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusOK)
	}
	var items []service.ConfigItem
	if err := json.Unmarshal(recorder.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal(response) error = %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("items = %d, want 5", len(items))
	}
	if items[0].Key != config.SettingSharingThumbnailSizes {
		t.Fatalf("first key = %q, want %q", items[0].Key, config.SettingSharingThumbnailSizes)
	}
}

func newConfigRouteHandler(database *gorm.DB) http.Handler {
	return newConfigRouteHandlerWithAuth(database, newRouterTestAuthService(database))
}

func newConfigRouteHandlerWithAuth(database *gorm.DB, authService *service.AuthService) http.Handler {
	configService := service.NewConfigService(repository.NewConfigRepository(database), config.Config{
		Sharing: config.SharingConfig{
			ThumbnailSizes:             []int{128, 256, 512},
			ThumbnailDefaultSize:       256,
			ThumbnailsGenerateForVideo: true,
			VideoInlineMaxSizeMB:       25,
			VideoPlaybackEnabled:       true,
		},
	})
	return New(
		config.Config{},
		buildinfo.Info{Version: "test"},
		authService,
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database)),
		service.NewInventoryService(repository.NewInventoryRepository(database)),
		nil,
		nil,
		nil,
		configService,
	)
}

func createConfigUserToken(t *testing.T, database *gorm.DB, permissions []model.Permission) string {
	t.Helper()

	hashedPassword, err := security.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword(user) error = %v", err)
	}
	role := model.Role{Name: "settings-role", Status: model.RoleStatusActive}
	if err := database.Create(&role).Error; err != nil {
		t.Fatalf("Create(role) error = %v", err)
	}
	for _, permission := range permissions {
		permission.RoleID = role.ID
		if err := database.Create(&permission).Error; err != nil {
			t.Fatalf("Create(permission) error = %v", err)
		}
	}
	user := model.User{Name: "settings-user", Status: model.UserStatusActive, Password: hashedPassword}
	if err := database.Create(&user).Error; err != nil {
		t.Fatalf("Create(user) error = %v", err)
	}
	if err := database.Create(&model.UserRole{UserID: user.ID, RoleID: role.ID}).Error; err != nil {
		t.Fatalf("Create(user role) error = %v", err)
	}

	authService := service.NewAuthService(
		repository.NewUserRepository(database),
		repository.NewUserTokenRepository(database),
		repository.NewNodeRepository(database),
		repository.NewNodeTokenRepository(database),
		"test-secret",
		30*time.Minute,
		8*time.Hour,
	)
	pair, err := authService.Login("settings-user", "secret")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	return pair.AccessToken
}

func configItemNumber(t *testing.T, items []service.ConfigItem, key string) int {
	t.Helper()
	for _, item := range items {
		if item.Key == key {
			value, ok := item.Value.(float64)
			if !ok {
				t.Fatalf("item %s has type %T", key, item.Value)
			}
			return int(value)
		}
	}
	t.Fatalf("missing item %s", key)
	return 0
}

func configItemNumberSlice(t *testing.T, items []service.ConfigItem, key string) []int {
	t.Helper()
	for _, item := range items {
		if item.Key != key {
			continue
		}
		values, ok := item.Value.([]any)
		if !ok {
			t.Fatalf("item %s has type %T", key, item.Value)
		}
		result := make([]int, 0, len(values))
		for _, value := range values {
			number, ok := value.(float64)
			if !ok {
				t.Fatalf("item %s contains type %T", key, value)
			}
			result = append(result, int(number))
		}
		return result
	}
	t.Fatalf("missing item %s", key)
	return nil
}

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"dropoutbox/internal/buildinfo"
	"dropoutbox/internal/config"
	"dropoutbox/internal/db"
	"dropoutbox/internal/model"
	"dropoutbox/internal/repository"
	"dropoutbox/internal/security"
	"dropoutbox/internal/service"

	"gorm.io/gorm"
)

func TestRequireAuthenticatedNodeAllowsNodeJWTAndSetsContext(t *testing.T) {
	database := openRouterTestDB(t)

	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	node := &model.Node{
		ID:     "node-a",
		Status: model.NodeStatusOffline,
		Secret: hashedSecret,
	}
	if err := database.Create(node).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}

	authService := newRouterTestAuthService(database)
	pair, err := authService.NodeLogin("node-a", "node-secret")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}

	var gotNodeID string
	handler := requireAuthenticatedNode(authService, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nodeID, ok := authenticatedNodeIDFromContext(r.Context())
		if !ok {
			t.Fatal("authenticated node id missing from context")
		}
		gotNodeID = nodeID
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/internal/test", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if gotNodeID != "node-a" {
		t.Fatalf("gotNodeID = %q, want %q", gotNodeID, "node-a")
	}
}

func TestRequireAuthenticatedNodeRejectsUserJWT(t *testing.T) {
	database := openRouterTestDB(t)

	hashedPassword, err := security.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	user := &model.User{
		Name:     "jsmith",
		Status:   model.UserStatusActive,
		Password: hashedPassword,
	}
	if err := database.Create(user).Error; err != nil {
		t.Fatalf("Create(user) error = %v", err)
	}

	authService := newRouterTestAuthService(database)
	pair, err := authService.Login("jsmith", "secret")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	handler := requireAuthenticatedNode(authService, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called for user jwt")
	}))

	req := httptest.NewRequest(http.MethodGet, "/internal/test", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestRequireAuthenticatedNodeRejectsDisabledNode(t *testing.T) {
	database := openRouterTestDB(t)

	token, err := security.GenerateNodeAccessToken([]byte("test-secret"), "node-a", time.Now().UTC().Add(30*time.Minute))
	if err != nil {
		t.Fatalf("GenerateNodeAccessToken() error = %v", err)
	}

	node := &model.Node{
		ID:     "node-a",
		Status: model.NodeStatusDisabled,
		Secret: "ignored",
	}
	if err := database.Create(node).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}

	authService := newRouterTestAuthService(database)

	handler := requireAuthenticatedNode(authService, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called for disabled node")
	}))

	req := httptest.NewRequest(http.MethodGet, "/internal/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestInternalAuthMeReturnsAuthenticatedNode(t *testing.T) {
	database := openRouterTestDB(t)

	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	node := &model.Node{
		ID:     "node-a",
		Status: model.NodeStatusOffline,
		Secret: hashedSecret,
	}
	if err := database.Create(node).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}

	authService := newRouterTestAuthService(database)
	pair, err := authService.NodeLogin("node-a", "node-secret")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}

	handler := New(
		config.Config{},
		buildinfo.Info{Version: "test", Commit: "test", BuildDate: "test"},
		authService,
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		service.NewNodeService(repository.NewNodeRepository(database)),
		service.NewInventoryService(repository.NewInventoryRepository(database)),
	)

	req := httptest.NewRequest(http.MethodGet, "/internal/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var body struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if body.ID != "node-a" {
		t.Fatalf("body.ID = %q, want %q", body.ID, "node-a")
	}
	if body.Status != string(model.NodeStatusOffline) {
		t.Fatalf("body.Status = %q, want %q", body.Status, model.NodeStatusOffline)
	}
}

func openRouterTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "router-auth.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	return database
}

func newRouterTestAuthService(database *gorm.DB) *service.AuthService {
	return service.NewAuthService(
		repository.NewUserRepository(database),
		repository.NewUserTokenRepository(database),
		repository.NewNodeRepository(database),
		repository.NewNodeTokenRepository(database),
		"test-secret",
		30*time.Minute,
		8*time.Hour,
	)
}

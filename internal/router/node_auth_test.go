package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
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
		service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database)),
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

func TestInternalNodesReportAvailabilityUpdatesNode(t *testing.T) {
	database := openRouterTestDB(t)

	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	node := &model.Node{
		ID:      "node-a",
		Status:  model.NodeStatusOffline,
		Secret:  hashedSecret,
		Address: "http://old-address:8081",
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
		service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database)),
		service.NewInventoryService(repository.NewInventoryRepository(database)),
	)

	req := httptest.NewRequest(http.MethodPost, "/internal/nodes", strings.NewReader(`{"address":"https://node-address:8081"}`))
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-API-Version", "1")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var body struct {
		NodeID   string `json:"node_id"`
		Address  string `json:"address"`
		LastSeen string `json:"last_seen"`
		Commands []any  `json:"commands"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if body.NodeID != "node-a" {
		t.Fatalf("body.NodeID = %q, want %q", body.NodeID, "node-a")
	}
	if body.Address != "https://node-address:8081" {
		t.Fatalf("body.Address = %q, want %q", body.Address, "https://node-address:8081")
	}
	if body.LastSeen == "" {
		t.Fatal("body.LastSeen is empty")
	}
	if len(body.Commands) != 0 {
		t.Fatalf("len(body.Commands) = %d, want 0", len(body.Commands))
	}

	var stored model.Node
	if err := database.First(&stored, "id = ?", "node-a").Error; err != nil {
		t.Fatalf("First(node) error = %v", err)
	}
	if stored.Address != "https://node-address:8081" {
		t.Fatalf("stored.Address = %q, want %q", stored.Address, "https://node-address:8081")
	}
	if stored.LastSeen == nil {
		t.Fatal("stored.LastSeen = nil, want timestamp")
	}
}

func TestInternalNodesReportAvailabilityReturnsPendingCommands(t *testing.T) {
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
	if err := database.Create(&model.Command{
		NodeID:  "node-a",
		Type:    model.NodeCommandTypeRefreshState,
		Status:  model.NodeCommandStatusPending,
		Payload: []byte(`{"placeholder":true}`),
	}).Error; err != nil {
		t.Fatalf("Create(node command) error = %v", err)
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
		service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database)),
		service.NewInventoryService(repository.NewInventoryRepository(database)),
	)

	req := httptest.NewRequest(http.MethodPost, "/internal/nodes", strings.NewReader(`{"address":"https://node-address:8081"}`))
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-API-Version", "1")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var body struct {
		Commands []struct {
			ID     uint   `json:"id"`
			Status string `json:"status"`
			Type   string `json:"type"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(body.Commands) != 1 {
		t.Fatalf("len(body.Commands) = %d, want 1", len(body.Commands))
	}
	if body.Commands[0].Type != string(model.NodeCommandTypeRefreshState) {
		t.Fatalf("body.Commands[0].Type = %q, want %q", body.Commands[0].Type, model.NodeCommandTypeRefreshState)
	}
	if body.Commands[0].Status != string(model.NodeCommandStatusPending) {
		t.Fatalf("body.Commands[0].Status = %q, want %q", body.Commands[0].Status, model.NodeCommandStatusPending)
	}
}

func TestInternalCommandsCompleteMarksOwnedCommandCompleted(t *testing.T) {
	database := openRouterTestDB(t)

	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	if err := database.Create(&model.Node{
		ID:     "node-a",
		Status: model.NodeStatusOffline,
		Secret: hashedSecret,
	}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	command := &model.Command{
		NodeID:  "node-a",
		Type:    model.NodeCommandTypeRefreshState,
		Status:  model.NodeCommandStatusPending,
		Payload: []byte(`{"placeholder":true}`),
	}
	if err := database.Create(command).Error; err != nil {
		t.Fatalf("Create(command) error = %v", err)
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
		service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database)),
		service.NewInventoryService(repository.NewInventoryRepository(database)),
	)

	req := httptest.NewRequest(http.MethodPost, "/internal/commands/"+strconv.FormatUint(uint64(command.ID), 10)+"/complete", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var body struct {
		ID     uint   `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if body.ID != command.ID {
		t.Fatalf("body.ID = %d, want %d", body.ID, command.ID)
	}
	if body.Status != string(model.NodeCommandStatusCompleted) {
		t.Fatalf("body.Status = %q, want %q", body.Status, model.NodeCommandStatusCompleted)
	}
}

func TestInternalReplicaFilesReportUpdatesCoordinatorState(t *testing.T) {
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

	inventory := &model.Inventory{
		Name:   "photos",
		Status: model.InventoryStatusOnline,
		Type:   model.InventoryTypeFolder,
	}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}

	file := &model.InventoryFile{
		InventoryID: inventory.ID,
		RelativeURI: "album/img.jpg",
		Status:      model.InventoryFileStatusActive,
		Size:        100,
		Hash:        "old-hash",
		Version:     3,
		Created:     time.Now().UTC().Add(-time.Hour),
		Modified:    time.Now().UTC().Add(-time.Minute),
	}
	if err := database.Create(file).Error; err != nil {
		t.Fatalf("Create(file) error = %v", err)
	}

	replicaA := &model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-a",
		URI:         "/data/a",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	replicaB := &model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-b",
		URI:         "/data/b",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(replicaA).Error; err != nil {
		t.Fatalf("Create(replicaA) error = %v", err)
	}
	if err := database.Create(replicaB).Error; err != nil {
		t.Fatalf("Create(replicaB) error = %v", err)
	}

	if err := database.Create(&model.ReplicaFile{
		FileID:    file.ID,
		ReplicaID: replicaA.ID,
		Version:   3,
		Status:    model.ReplicaFileStatusSynchronized,
	}).Error; err != nil {
		t.Fatalf("Create(replicaFileA) error = %v", err)
	}
	if err := database.Create(&model.ReplicaFile{
		FileID:    file.ID,
		ReplicaID: replicaB.ID,
		Version:   3,
		Status:    model.ReplicaFileStatusSynchronized,
	}).Error; err != nil {
		t.Fatalf("Create(replicaFileB) error = %v", err)
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
		service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database)),
		service.NewInventoryService(repository.NewInventoryRepository(database)),
	)

	req := httptest.NewRequest(http.MethodPost, "/internal/replica/"+strconv.FormatUint(uint64(replicaA.ID), 10)+"/files", strings.NewReader(`{"files":[{"file_id":`+strconv.FormatUint(uint64(file.ID), 10)+`,"file_size":200,"file_hash":"new-hash","modified_time":"2026-05-21T12:00:00Z"}]}`))
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-API-Version", "1")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusNoContent, recorder.Body.String())
	}

	var updatedFile model.InventoryFile
	if err := database.First(&updatedFile, file.ID).Error; err != nil {
		t.Fatalf("First(updatedFile) error = %v", err)
	}
	if updatedFile.Version != 4 {
		t.Fatalf("updatedFile.Version = %d, want 4", updatedFile.Version)
	}

	var pendingReplica model.ReplicaFile
	if err := database.Where("file_id = ? AND replica_id = ?", file.ID, replicaB.ID).First(&pendingReplica).Error; err != nil {
		t.Fatalf("First(pendingReplica) error = %v", err)
	}
	if pendingReplica.Status != model.ReplicaFileStatusPending {
		t.Fatalf("pendingReplica.Status = %q, want %q", pendingReplica.Status, model.ReplicaFileStatusPending)
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

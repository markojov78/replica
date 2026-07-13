package router

import (
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
	"replica/internal/service"

	"github.com/gorilla/websocket"
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
	pair, err := authService.NodeLogin("node-a", "node-secret", "")
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

func TestInternalNodeLoginStoresPublicKey(t *testing.T) {
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

	handler := newInternalAuthTestHandler(t, database)
	req := httptest.NewRequest(http.MethodPost, "/node/auth/login", strings.NewReader(`{"node_id":"node-a","secret":"node-secret","public_key":"node-public-key"}`))
	req.Header.Set("X-API-Version", "1")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var stored model.Node
	if err := database.First(&stored, "id = ?", "node-a").Error; err != nil {
		t.Fatalf("First(node) error = %v", err)
	}
	if stored.PublicKey != "node-public-key" {
		t.Fatalf("stored.PublicKey = %q, want node-public-key", stored.PublicKey)
	}
	if stored.Address != "" {
		t.Fatalf("stored.Address = %q, want empty", stored.Address)
	}
	if stored.LastSeen != nil {
		t.Fatalf("stored.LastSeen = %v, want nil", stored.LastSeen)
	}
	if stored.Status != model.NodeStatusOffline {
		t.Fatalf("stored.Status = %q, want %q", stored.Status, model.NodeStatusOffline)
	}
}

func TestInternalValidateUserTokenAcceptsActiveUserToken(t *testing.T) {
	database := openRouterTestDB(t)
	handler := newInternalAuthTestHandler(t, database)
	userToken, nodeToken := createValidateUserTokenCredentials(t, database, model.UserStatusActive)

	req := httptest.NewRequest(http.MethodPost, "/node/auth/validate-user-token", strings.NewReader(`{"access_token":`+strconv.Quote(userToken)+`}`))
	req.Header.Set("Authorization", "Bearer "+nodeToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusOK)
	}
	var body struct {
		UserID   uint   `json:"user_id"`
		Username string `json:"username"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal(response) error = %v", err)
	}
	if body.UserID == 0 || body.Username == "" || body.Status != "active" {
		t.Fatalf("body = %+v, want active user with username", body)
	}
}

func TestInternalValidateUserTokenRejectsNodeToken(t *testing.T) {
	database := openRouterTestDB(t)
	handler := newInternalAuthTestHandler(t, database)
	_, nodeToken := createValidateUserTokenCredentials(t, database, model.UserStatusActive)

	req := httptest.NewRequest(http.MethodPost, "/node/auth/validate-user-token", strings.NewReader(`{"access_token":`+strconv.Quote(nodeToken)+`}`))
	req.Header.Set("Authorization", "Bearer "+nodeToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusUnauthorized)
	}
}

func TestInternalValidateUserTokenRejectsExpiredToken(t *testing.T) {
	database := openRouterTestDB(t)
	handler := newInternalAuthTestHandler(t, database)
	_, nodeToken := createValidateUserTokenCredentials(t, database, model.UserStatusActive)
	expiredToken, err := security.GenerateUserAccessToken([]byte("test-secret"), 1, 1, time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatalf("GenerateUserAccessToken() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/node/auth/validate-user-token", strings.NewReader(`{"access_token":`+strconv.Quote(expiredToken)+`}`))
	req.Header.Set("Authorization", "Bearer "+nodeToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusUnauthorized)
	}
}

func TestInternalValidateUserTokenRejectsInactiveUser(t *testing.T) {
	database := openRouterTestDB(t)
	handler := newInternalAuthTestHandler(t, database)
	userToken, nodeToken := createValidateUserTokenCredentials(t, database, model.UserStatusDeleted)

	req := httptest.NewRequest(http.MethodPost, "/node/auth/validate-user-token", strings.NewReader(`{"access_token":`+strconv.Quote(userToken)+`}`))
	req.Header.Set("Authorization", "Bearer "+nodeToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusForbidden)
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
	pair, err := authService.NodeLogin("node-a", "node-secret", "")
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
		newRouterTestReplicaService(database, nil),
		service.NewShareService(repository.NewShareRepository(database), nil),
		nil,
	)

	req := httptest.NewRequest(http.MethodGet, "/node/auth/me", nil)
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
	pair, err := authService.NodeLogin("node-a", "node-secret", "")
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
		newRouterTestReplicaService(database, nil),
		service.NewShareService(repository.NewShareRepository(database), nil),
		nil,
	)

	req := httptest.NewRequest(http.MethodPost, "/node/nodes", strings.NewReader(`{"address":"https://node-address:8081","interval":60}`))
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
	if stored.PublicKey != "" {
		t.Fatalf("stored.PublicKey = %q, want empty", stored.PublicKey)
	}
	if stored.LastSeen == nil {
		t.Fatal("stored.LastSeen = nil, want timestamp")
	}
	if stored.Interval == nil || *stored.Interval != 60 {
		t.Fatalf("stored.Interval = %v, want 60", stored.Interval)
	}
	if stored.Status != model.NodeStatusUnreachable {
		t.Fatalf("stored.Status = %q, want %q", stored.Status, model.NodeStatusUnreachable)
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
	pair, err := authService.NodeLogin("node-a", "node-secret", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}

	nodeService := service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	inventoryRepo := repository.NewInventoryRepository(database)
	handler := New(
		config.Config{},
		buildinfo.Info{Version: "test", Commit: "test", BuildDate: "test"},
		authService,
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		nodeService,
		service.NewInventoryService(inventoryRepo),
		service.NewReplicaService(repository.NewReplicaRepository(database), inventoryRepo, nodeService),
		service.NewShareService(repository.NewShareRepository(database), nil),
		nil,
	)

	req := httptest.NewRequest(http.MethodPost, "/node/nodes", strings.NewReader(`{"address":"https://node-address:8081","interval":60}`))
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

func TestInternalReplicasReturnsOnlyAuthenticatedNodeReplicas(t *testing.T) {
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
		t.Fatalf("Create(node-a) error = %v", err)
	}
	if err := database.Create(&model.Node{
		ID:     "node-b",
		Status: model.NodeStatusOffline,
		Secret: "ignored",
	}).Error; err != nil {
		t.Fatalf("Create(node-b) error = %v", err)
	}

	inventory := &model.Inventory{
		Name:   "photos",
		Status: model.InventoryStatusActive,
		Type:   model.InventoryTypeFolder,
	}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	replicaA := model.Replica{
		InventoryID:    inventory.ID,
		NodeID:         "node-a",
		URI:            "/data/a",
		Status:         model.ReplicaStatusActive,
		Type:           model.ReplicaTypeFilesystem,
		StorageProfile: "aws",
	}
	if err := database.Create(&replicaA).Error; err != nil {
		t.Fatalf("Create(replicaA) error = %v", err)
	}
	if err := database.Create(&model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-b",
		URI:         "/data/b",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}).Error; err != nil {
		t.Fatalf("Create(replicaB) error = %v", err)
	}
	inventoryFile := model.InventoryFile{
		InventoryID: inventory.ID,
		RelativeURI: "pending.txt",
		Status:      model.InventoryFileStatusActive,
	}
	if err := database.Create(&inventoryFile).Error; err != nil {
		t.Fatalf("Create(inventory file) error = %v", err)
	}
	if err := database.Create(&model.ReplicaFile{
		FileID:    inventoryFile.ID,
		ReplicaID: replicaA.ID,
		Status:    model.ReplicaFileStatusPending,
	}).Error; err != nil {
		t.Fatalf("Create(replica file) error = %v", err)
	}

	authService := newRouterTestAuthService(database)
	pair, err := authService.NodeLogin("node-a", "node-secret", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}

	nodeService := service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	inventoryService := service.NewInventoryService(repository.NewInventoryRepository(database), nodeService)
	replicaService := newRouterTestReplicaService(database, nodeService)

	handler := New(
		config.Config{},
		buildinfo.Info{Version: "test", Commit: "test", BuildDate: "test"},
		authService,
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		nodeService,
		inventoryService,
		replicaService,
		service.NewShareService(repository.NewShareRepository(database), nil),
		nil,
	)

	req := httptest.NewRequest(http.MethodGet, "/node/replicas", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var body []struct {
		NodeID         string `json:"node_id"`
		URI            string `json:"uri"`
		InventoryType  string `json:"inventory_type"`
		StorageProfile string `json:"storage_profile"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("len(body) = %d, want 1", len(body))
	}
	if body[0].NodeID != "node-a" {
		t.Fatalf("body[0].NodeID = %q, want %q", body[0].NodeID, "node-a")
	}
	if body[0].URI != "/data/a" {
		t.Fatalf("body[0].URI = %q, want %q", body[0].URI, "/data/a")
	}
	if body[0].InventoryType != string(model.InventoryTypeFolder) {
		t.Fatalf("body[0].InventoryType = %q, want %q", body[0].InventoryType, model.InventoryTypeFolder)
	}
	if body[0].StorageProfile != "aws" {
		t.Fatalf("body[0].StorageProfile = %q, want aws", body[0].StorageProfile)
	}
	var rawBody []map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &rawBody); err != nil {
		t.Fatalf("Unmarshal(raw) error = %v", err)
	}
	if _, ok := rawBody[0]["sync_status"]; ok {
		t.Fatalf("/node/replicas response contains sync_status: %s", recorder.Body.String())
	}
}

func TestInternalSharesReturnsOnlyAuthenticatedNodeShares(t *testing.T) {
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
		t.Fatalf("Create(node-a) error = %v", err)
	}
	if err := database.Create(&model.Node{
		ID:     "node-b",
		Status: model.NodeStatusOffline,
		Secret: "ignored",
	}).Error; err != nil {
		t.Fatalf("Create(node-b) error = %v", err)
	}

	inventory := &model.Inventory{
		Name:   "photos",
		Status: model.InventoryStatusActive,
		Type:   model.InventoryTypeFolder,
	}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	replicaA := &model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-a",
		URI:         "/data/a",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(replicaA).Error; err != nil {
		t.Fatalf("Create(replicaA) error = %v", err)
	}
	replicaB := &model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-b",
		URI:         "/data/b",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(replicaB).Error; err != nil {
		t.Fatalf("Create(replicaB) error = %v", err)
	}

	user := &model.User{Name: "share-user", Status: model.UserStatusActive}
	if err := database.Create(user).Error; err != nil {
		t.Fatalf("Create(user) error = %v", err)
	}
	linkHash := "ImyZbX8zv0UrsCB7Rthq9R7nQMMKRyhT"
	expiresAt := time.Date(2026, 3, 17, 10, 30, 0, 0, time.UTC)
	view := "grid"
	pageSize := 100
	thumbnailSize := 256
	theme := "dark"
	shareA := &model.Share{
		ReplicaID:       replicaA.ID,
		Name:            "Vacation March 2026",
		Status:          model.ShareStatusActive,
		LinkHash:        &linkHash,
		ShareExpiration: &expiresAt,
		Properties: model.ShareProperties{
			View:          &view,
			PageSize:      &pageSize,
			ThumbnailSize: &thumbnailSize,
			Theme:         &theme,
		},
	}
	if err := database.Create(shareA).Error; err != nil {
		t.Fatalf("Create(shareA) error = %v", err)
	}
	shareB := &model.Share{
		ReplicaID: replicaB.ID,
		Name:      "Other node share",
		Status:    model.ShareStatusActive,
	}
	if err := database.Create(shareB).Error; err != nil {
		t.Fatalf("Create(shareB) error = %v", err)
	}
	userID := user.ID
	shareUser := &model.ShareUser{UserID: &userID, ShareID: shareA.ID}
	if err := database.Create(shareUser).Error; err != nil {
		t.Fatalf("Create(share_user) error = %v", err)
	}
	if err := database.Create(&model.SharePermission{ShareUserID: shareUser.ID, Permission: string(model.PermissionActionRead)}).Error; err != nil {
		t.Fatalf("Create(share_permission) error = %v", err)
	}
	anonymousShareUser := &model.ShareUser{ShareID: shareA.ID, Anonymous: true}
	if err := database.Create(anonymousShareUser).Error; err != nil {
		t.Fatalf("Create(anonymous share_user) error = %v", err)
	}
	if err := database.Create(&model.SharePermission{ShareUserID: anonymousShareUser.ID, Permission: string(model.PermissionActionRead)}).Error; err != nil {
		t.Fatalf("Create(anonymous share_permission) error = %v", err)
	}

	authService := newRouterTestAuthService(database)
	pair, err := authService.NodeLogin("node-a", "node-secret", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}

	nodeService := service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	inventoryService := service.NewInventoryService(repository.NewInventoryRepository(database), nodeService)
	replicaService := newRouterTestReplicaService(database, nodeService)
	shareService := service.NewShareService(repository.NewShareRepository(database), nil)

	handler := New(
		config.Config{},
		buildinfo.Info{Version: "test", Commit: "test", BuildDate: "test"},
		authService,
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		nodeService,
		inventoryService,
		replicaService,
		shareService,
		nil,
	)

	req := httptest.NewRequest(http.MethodGet, "/node/shares", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var body []service.ShareDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("len(body) = %d, want 1; body=%+v", len(body), body)
	}
	if body[0].ID != shareA.ID || body[0].InventoryID != inventory.ID || body[0].ReplicaID != replicaA.ID {
		t.Fatalf("body[0] = %+v, want share=%d inventory=%d replica=%d", body[0], shareA.ID, inventory.ID, replicaA.ID)
	}
	if body[0].Name != "Vacation March 2026" || body[0].Status != string(model.ShareStatusActive) {
		t.Fatalf("body[0] = %+v, want active Vacation March 2026", body[0])
	}
	if body[0].LinkHash == nil || *body[0].LinkHash != linkHash {
		t.Fatalf("body[0].LinkHash = %v, want %q", body[0].LinkHash, linkHash)
	}
	if body[0].ShareExpiration == nil || !body[0].ShareExpiration.Equal(expiresAt) {
		t.Fatalf("body[0].ShareExpiration = %v, want %v", body[0].ShareExpiration, expiresAt)
	}
	if body[0].Properties.View == nil || *body[0].Properties.View != view ||
		body[0].Properties.PageSize == nil || *body[0].Properties.PageSize != pageSize ||
		body[0].Properties.ThumbnailSize == nil || *body[0].Properties.ThumbnailSize != thumbnailSize ||
		body[0].Properties.Theme == nil || *body[0].Properties.Theme != theme {
		t.Fatalf("body[0].Properties = %+v, want assigned share properties", body[0].Properties)
	}
	if len(body[0].UserPermissions) != 1 || body[0].UserPermissions[0].UserID != user.ID || len(body[0].UserPermissions[0].Permissions) != 1 {
		t.Fatalf("body[0].UserPermissions = %+v, want user read", body[0].UserPermissions)
	}
	if len(body[0].AnonymousPermissions) != 1 || body[0].AnonymousPermissions[0] != string(model.PermissionActionRead) {
		t.Fatalf("body[0].AnonymousPermissions = %+v, want read", body[0].AnonymousPermissions)
	}
}

func TestInternalSharesReturnsEmptyListWhenNodeHasNoShares(t *testing.T) {
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
		t.Fatalf("Create(node-a) error = %v", err)
	}

	authService := newRouterTestAuthService(database)
	pair, err := authService.NodeLogin("node-a", "node-secret", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}

	nodeService := service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	handler := New(
		config.Config{},
		buildinfo.Info{Version: "test", Commit: "test", BuildDate: "test"},
		authService,
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		nodeService,
		service.NewInventoryService(repository.NewInventoryRepository(database), nodeService),
		newRouterTestReplicaService(database, nodeService),
		service.NewShareService(repository.NewShareRepository(database), nil),
		nil,
	)

	req := httptest.NewRequest(http.MethodGet, "/node/shares", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var body []service.ShareDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(body) != 0 {
		t.Fatalf("len(body) = %d, want 0; body=%+v", len(body), body)
	}
}

func TestInternalSharesRequiresNodeToken(t *testing.T) {
	database := openRouterTestDB(t)
	handler := New(
		config.Config{},
		buildinfo.Info{Version: "test", Commit: "test", BuildDate: "test"},
		newRouterTestAuthService(database),
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database)),
		service.NewInventoryService(repository.NewInventoryRepository(database)),
		nil,
		service.NewShareService(repository.NewShareRepository(database), nil),
		nil,
	)

	req := httptest.NewRequest(http.MethodGet, "/node/shares", nil)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
	}
}

func TestInternalReplicaFilesReturnsInventoryAndReplicaMetadata(t *testing.T) {
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

	inventory := &model.Inventory{
		Name:   "photos",
		Status: model.InventoryStatusActive,
		Type:   model.InventoryTypeFolder,
	}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}

	replica := &model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-a",
		URI:         "/data/photos",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(replica).Error; err != nil {
		t.Fatalf("Create(replica) error = %v", err)
	}

	created := time.Date(2026, 5, 21, 11, 0, 0, 0, time.UTC)
	modified := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	file := &model.InventoryFile{
		InventoryID: inventory.ID,
		RelativeURI: "album/img.jpg",
		Status:      model.InventoryFileStatusActive,
		Size:        200,
		Hash:        "inventory-hash",
		Version:     5,
		Created:     created,
		Modified:    modified,
	}
	if err := database.Create(file).Error; err != nil {
		t.Fatalf("Create(file) error = %v", err)
	}

	replicaFile := &model.ReplicaFile{
		FileID:    file.ID,
		ReplicaID: replica.ID,
		Version:   4,
		Status:    model.ReplicaFileStatusPending,
	}
	if err := database.Create(replicaFile).Error; err != nil {
		t.Fatalf("Create(replicaFile) error = %v", err)
	}

	authService := newRouterTestAuthService(database)
	pair, err := authService.NodeLogin("node-a", "node-secret", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}

	nodeService := service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	inventoryService := service.NewInventoryService(repository.NewInventoryRepository(database), nodeService)
	replicaService := newRouterTestReplicaService(database, nodeService)

	handler := New(
		config.Config{},
		buildinfo.Info{Version: "test", Commit: "test", BuildDate: "test"},
		authService,
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		nodeService,
		inventoryService,
		replicaService,
		service.NewShareService(repository.NewShareRepository(database), nil),
		nil,
	)

	req := httptest.NewRequest(http.MethodGet, "/node/replica/"+strconv.FormatUint(uint64(replica.ID), 10)+"/files", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var body struct {
		Files []struct {
			FileID           uint      `json:"file_id"`
			ReplicaID        uint      `json:"replica_id"`
			InventoryID      uint      `json:"inventory_id"`
			RelativeURI      string    `json:"relative_uri"`
			Size             int64     `json:"size"`
			Hash             string    `json:"hash"`
			InventoryStatus  string    `json:"inventory_status"`
			InventoryVersion uint      `json:"inventory_version"`
			ReplicaStatus    string    `json:"replica_status"`
			ReplicaVersion   uint      `json:"replica_version"`
			Created          time.Time `json:"created"`
			Modified         time.Time `json:"modified"`
		} `json:"files"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(body.Files) != 1 {
		t.Fatalf("len(body.Files) = %d, want 1", len(body.Files))
	}
	got := body.Files[0]
	if got.FileID != file.ID || got.ReplicaID != replica.ID || got.InventoryID != inventory.ID {
		t.Fatalf("ids = file:%d replica:%d inventory:%d, want %d/%d/%d", got.FileID, got.ReplicaID, got.InventoryID, file.ID, replica.ID, inventory.ID)
	}
	if got.RelativeURI != "album/img.jpg" || got.Size != 200 || got.Hash != "inventory-hash" {
		t.Fatalf("file metadata = uri:%q size:%d hash:%q", got.RelativeURI, got.Size, got.Hash)
	}
	if got.InventoryStatus != string(model.InventoryFileStatusActive) || got.InventoryVersion != 5 {
		t.Fatalf("inventory state = %s/%d, want active/5", got.InventoryStatus, got.InventoryVersion)
	}
	if got.ReplicaStatus != string(model.ReplicaFileStatusPending) || got.ReplicaVersion != 4 {
		t.Fatalf("replica state = %s/%d, want pending/4", got.ReplicaStatus, got.ReplicaVersion)
	}
}

func TestInternalReplicaFilesFiltersByStatus(t *testing.T) {
	database := openRouterTestDB(t)
	handler, accessToken, replica, pendingFile := newInternalReplicaFilesFilterTestHandler(t, database)

	req := httptest.NewRequest(http.MethodGet, "/node/replica/"+strconv.FormatUint(uint64(replica.ID), 10)+"/files?status=pending", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var body struct {
		Files []struct {
			FileID        uint   `json:"file_id"`
			ReplicaStatus string `json:"replica_status"`
		} `json:"files"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(body.Files) != 1 {
		t.Fatalf("len(body.Files) = %d, want 1", len(body.Files))
	}
	if body.Files[0].FileID != pendingFile.ID || body.Files[0].ReplicaStatus != string(model.ReplicaFileStatusPending) {
		t.Fatalf("file = %+v, want pending file_id=%d", body.Files[0], pendingFile.ID)
	}
}

func TestInternalReplicaFilesRejectsInvalidStatusFilter(t *testing.T) {
	database := openRouterTestDB(t)
	handler, accessToken, replica, _ := newInternalReplicaFilesFilterTestHandler(t, database)

	req := httptest.NewRequest(http.MethodGet, "/node/replica/"+strconv.FormatUint(uint64(replica.ID), 10)+"/files?status=invalid", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}

func TestPublicReplicasListIsPaginated(t *testing.T) {
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
	role := &model.Role{
		Name:   "inventory-reader",
		Status: model.RoleStatusActive,
	}
	if err := database.Create(role).Error; err != nil {
		t.Fatalf("Create(role) error = %v", err)
	}
	permissions := []model.Permission{
		{RoleID: role.ID, Resource: model.PermissionResourceInventories, Action: model.PermissionActionRead},
	}
	if err := database.Create(&permissions).Error; err != nil {
		t.Fatalf("Create(permissions) error = %v", err)
	}
	if err := database.Create(&model.UserRole{UserID: user.ID, RoleID: role.ID}).Error; err != nil {
		t.Fatalf("Create(user_role) error = %v", err)
	}

	inventory := &model.Inventory{Name: "photos", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	for i, uri := range []string{"/data/a", "/data/b"} {
		storageProfile := ""
		if i == 0 {
			storageProfile = "aws"
		}
		if err := database.Create(&model.Replica{
			InventoryID:    inventory.ID,
			NodeID:         "node-a",
			URI:            uri,
			Status:         model.ReplicaStatusActive,
			Type:           model.ReplicaTypeFilesystem,
			StorageProfile: storageProfile,
		}).Error; err != nil {
			t.Fatalf("Create(replica %s) error = %v", uri, err)
		}
	}

	authService := newRouterTestAuthService(database)
	pair, err := authService.Login("jsmith", "secret")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	nodeService := service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	inventoryService := service.NewInventoryService(repository.NewInventoryRepository(database), nodeService)
	replicaService := newRouterTestReplicaService(database, nodeService)
	handler := New(
		config.Config{},
		buildinfo.Info{Version: "test", Commit: "test", BuildDate: "test"},
		authService,
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		nodeService,
		inventoryService,
		replicaService,
		service.NewShareService(repository.NewShareRepository(database), nil),
		nil,
	)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/replicas?page=1&count=1", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var body struct {
		Items []struct {
			URI            string `json:"uri"`
			StorageProfile string `json:"storage_profile"`
		} `json:"items"`
		Page  int   `json:"page"`
		Count int   `json:"count"`
		Total int64 `json:"total"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("len(body.Items) = %d, want 1", len(body.Items))
	}
	if body.Page != 1 || body.Count != 1 || body.Total != 2 {
		t.Fatalf("pagination = page:%d count:%d total:%d, want 1/1/2", body.Page, body.Count, body.Total)
	}
	if body.Items[0].StorageProfile != "aws" {
		t.Fatalf("body.Items[0].StorageProfile = %q, want aws", body.Items[0].StorageProfile)
	}
}

func TestInternalNodesWebSocketAcceptsAuthenticatedNode(t *testing.T) {
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
	pair, err := authService.NodeLogin("node-a", "node-secret", "")
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
		nil,
		service.NewShareService(repository.NewShareRepository(database), nil),
		nil,
	)

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	wsURL.Scheme = "ws"
	wsURL.Path = "/node/nodes/ws"

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+pair.AccessToken)

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL.String(), headers)
	if err != nil {
		if resp != nil {
			t.Fatalf("Dial() error = %v; status=%d", err, resp.StatusCode)
		}
		t.Fatalf("Dial() error = %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSwitchingProtocols)
	}

	waitForStoredNodeStatus(t, database, "node-a", model.NodeStatusOnline)
	if err := conn.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	waitForStoredNodeStatus(t, database, "node-a", model.NodeStatusOffline)
}

func TestInventoryCreatePushesPendingScanReplicaCommandToNodeWebSocket(t *testing.T) {
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

	hashedPassword, err := security.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := database.Create(&model.User{
		Name:     "jsmith",
		Status:   model.UserStatusActive,
		Password: hashedPassword,
	}).Error; err != nil {
		t.Fatalf("Create(user) error = %v", err)
	}
	role := &model.Role{
		Name:   "inventory-admin",
		Status: model.RoleStatusActive,
	}
	if err := database.Create(role).Error; err != nil {
		t.Fatalf("Create(role) error = %v", err)
	}
	permissions := []model.Permission{
		{RoleID: role.ID, Resource: model.PermissionResourceInventories, Action: model.PermissionActionCreate},
		{RoleID: role.ID, Resource: model.PermissionResourceInventories, Action: model.PermissionActionRead},
	}
	if err := database.Create(&permissions).Error; err != nil {
		t.Fatalf("Create(permissions) error = %v", err)
	}
	if err := database.Create(&model.UserRole{UserID: 1, RoleID: role.ID}).Error; err != nil {
		t.Fatalf("Create(user_role) error = %v", err)
	}

	authService := newRouterTestAuthService(database)
	userPair, err := authService.Login("jsmith", "secret")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	nodePair, err := authService.NodeLogin("node-a", "node-secret", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}

	nodeService := service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	inventoryService := service.NewInventoryService(repository.NewInventoryRepository(database), nodeService)

	handler := New(
		config.Config{},
		buildinfo.Info{Version: "test", Commit: "test", BuildDate: "test"},
		authService,
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		nodeService,
		inventoryService,
		nil,
		service.NewShareService(repository.NewShareRepository(database), nil),
		nil,
	)

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	wsURL.Scheme = "ws"
	wsURL.Path = "/node/nodes/ws"

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+nodePair.AccessToken)

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL.String(), headers)
	if err != nil {
		if resp != nil {
			t.Fatalf("Dial() error = %v; status=%d", err, resp.StatusCode)
		}
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()

	createReq := httptest.NewRequest(http.MethodPost, "/api/admin/inventories", strings.NewReader(`{"name":"Photos","node_id":"node-a","folder_uri":"/data/photos"}`))
	createReq.Header.Set("Authorization", "Bearer "+userPair.AccessToken)
	createReq.Header.Set("X-API-Version", "1")
	createReq.Header.Set("Content-Type", "application/json")
	createRecorder := httptest.NewRecorder()

	handler.ServeHTTP(createRecorder, createReq)

	if createRecorder.Code != http.StatusCreated && createRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 or 201; body=%s", createRecorder.Code, createRecorder.Body.String())
	}

	var command struct {
		ID      uint   `json:"id"`
		NodeID  string `json:"node_id"`
		Type    string `json:"type"`
		Status  string `json:"status"`
		Payload struct {
			ReplicaID uint `json:"replica_id"`
		} `json:"payload"`
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	if err := conn.ReadJSON(&command); err != nil {
		t.Fatalf("ReadJSON() error = %v", err)
	}
	if command.NodeID != "node-a" {
		t.Fatalf("command.NodeID = %q, want %q", command.NodeID, "node-a")
	}
	if command.Type != string(model.NodeCommandTypeScanReplica) {
		t.Fatalf("command.Type = %q, want %q", command.Type, model.NodeCommandTypeScanReplica)
	}
	if command.Status != string(model.NodeCommandStatusPending) {
		t.Fatalf("command.Status = %q, want %q", command.Status, model.NodeCommandStatusPending)
	}
	if command.Payload.ReplicaID == 0 {
		t.Fatal("command.Payload.ReplicaID = 0, want created replica id")
	}
	var refreshCommand struct {
		NodeID string `json:"node_id"`
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	if err := conn.ReadJSON(&refreshCommand); err != nil {
		t.Fatalf("ReadJSON(refresh_state) error = %v", err)
	}
	if refreshCommand.NodeID != "node-a" {
		t.Fatalf("refreshCommand.NodeID = %q, want %q", refreshCommand.NodeID, "node-a")
	}
	if refreshCommand.Type != string(model.NodeCommandTypeRefreshState) {
		t.Fatalf("refreshCommand.Type = %q, want %q", refreshCommand.Type, model.NodeCommandTypeRefreshState)
	}
	if refreshCommand.Status != string(model.NodeCommandStatusPending) {
		t.Fatalf("refreshCommand.Status = %q, want %q", refreshCommand.Status, model.NodeCommandStatusPending)
	}

	reportReq := httptest.NewRequest(http.MethodPost, "/node/nodes", strings.NewReader(`{"address":"https://node-address:8081","interval":60}`))
	reportReq.Header.Set("Authorization", "Bearer "+nodePair.AccessToken)
	reportReq.Header.Set("X-API-Version", "1")
	reportReq.Header.Set("Content-Type", "application/json")
	reportRecorder := httptest.NewRecorder()

	handler.ServeHTTP(reportRecorder, reportReq)

	if reportRecorder.Code != http.StatusOK {
		t.Fatalf("heartbeat status = %d, want %d; body=%s", reportRecorder.Code, http.StatusOK, reportRecorder.Body.String())
	}

	var heartbeat struct {
		Commands []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Payload struct {
				ReplicaID uint `json:"replica_id"`
			} `json:"payload"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(reportRecorder.Body.Bytes(), &heartbeat); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(heartbeat.Commands) != 2 {
		t.Fatalf("len(heartbeat.Commands) = %d, want 2", len(heartbeat.Commands))
	}
	if heartbeat.Commands[0].Type != string(model.NodeCommandTypeScanReplica) {
		t.Fatalf("heartbeat.Commands[0].Type = %q, want %q", heartbeat.Commands[0].Type, model.NodeCommandTypeScanReplica)
	}
	if heartbeat.Commands[0].Status != string(model.NodeCommandStatusPending) {
		t.Fatalf("heartbeat.Commands[0].Status = %q, want %q", heartbeat.Commands[0].Status, model.NodeCommandStatusPending)
	}
	if heartbeat.Commands[0].Payload.ReplicaID != command.Payload.ReplicaID {
		t.Fatalf("heartbeat.Commands[0].Payload.ReplicaID = %d, want %d", heartbeat.Commands[0].Payload.ReplicaID, command.Payload.ReplicaID)
	}
	if heartbeat.Commands[1].Type != string(model.NodeCommandTypeRefreshState) {
		t.Fatalf("heartbeat.Commands[1].Type = %q, want %q", heartbeat.Commands[1].Type, model.NodeCommandTypeRefreshState)
	}
	if heartbeat.Commands[1].Status != string(model.NodeCommandStatusPending) {
		t.Fatalf("heartbeat.Commands[1].Status = %q, want %q", heartbeat.Commands[1].Status, model.NodeCommandStatusPending)
	}
}

func TestPublicReplicaCreatePopulatesPendingFilesAndReconcileCommand(t *testing.T) {
	database := openRouterTestDB(t)

	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := database.Create(&[]model.Node{
		{ID: "node-a", Status: model.NodeStatusOffline, Secret: hashedSecret, Address: "https://node-a.example"},
		{ID: "node-b", Status: model.NodeStatusOffline, Secret: hashedSecret, Address: "https://node-b.example"},
	}).Error; err != nil {
		t.Fatalf("Create(nodes) error = %v", err)
	}

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
	role := &model.Role{
		Name:   "inventory-updater",
		Status: model.RoleStatusActive,
	}
	if err := database.Create(role).Error; err != nil {
		t.Fatalf("Create(role) error = %v", err)
	}
	if err := database.Create(&model.Permission{RoleID: role.ID, Resource: model.PermissionResourceInventories, Action: model.PermissionActionUpdate}).Error; err != nil {
		t.Fatalf("Create(permission) error = %v", err)
	}
	if err := database.Create(&model.UserRole{UserID: user.ID, RoleID: role.ID}).Error; err != nil {
		t.Fatalf("Create(user_role) error = %v", err)
	}

	inventory := &model.Inventory{Name: "photos", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	files := []model.InventoryFile{
		{InventoryID: inventory.ID, RelativeURI: "one.jpg", Status: model.InventoryFileStatusActive, Version: 3, Created: time.Now().UTC(), Modified: time.Now().UTC()},
		{InventoryID: inventory.ID, RelativeURI: "two.jpg", Status: model.InventoryFileStatusActive, Version: 4, Created: time.Now().UTC(), Modified: time.Now().UTC()},
	}
	if err := database.Create(&files).Error; err != nil {
		t.Fatalf("Create(files) error = %v", err)
	}
	sourceReplica := model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-a",
		URI:         "/data/photos",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(&sourceReplica).Error; err != nil {
		t.Fatalf("Create(sourceReplica) error = %v", err)
	}
	if err := database.Create(&[]model.ReplicaFile{
		{FileID: files[0].ID, ReplicaID: sourceReplica.ID, Version: 3, Status: model.ReplicaFileStatusSynchronized},
		{FileID: files[1].ID, ReplicaID: sourceReplica.ID, Version: 4, Status: model.ReplicaFileStatusSynchronized},
	}).Error; err != nil {
		t.Fatalf("Create(sourceReplicaFiles) error = %v", err)
	}
	settingService := service.NewSettingService(repository.NewSettingRepository(database))
	if err := settingService.EnsureTransferKeys(); err != nil {
		t.Fatalf("EnsureTransferKeys() error = %v", err)
	}

	authService := newRouterTestAuthService(database)
	pair, err := authService.Login("jsmith", "secret")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	nodeService := service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	inventoryRepo := repository.NewInventoryRepository(database)
	handler := New(
		config.Config{},
		buildinfo.Info{Version: "test", Commit: "test", BuildDate: "test"},
		authService,
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		nodeService,
		service.NewInventoryService(inventoryRepo, nodeService),
		service.NewReplicaService(repository.NewReplicaRepository(database), inventoryRepo, nodeService, settingService),
		service.NewShareService(repository.NewShareRepository(database), nil),
		nil,
	)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/replicas", strings.NewReader(`{"inventory_id":`+strconv.FormatUint(uint64(inventory.ID), 10)+`,"node_id":"node-b","uri":"s3://bucket/photos","type":"storage","storage_profile":"aws"}`))
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-API-Version", "1")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK && recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 200 or 201; body=%s", recorder.Code, recorder.Body.String())
	}
	var createdBody struct {
		StorageProfile string `json:"storage_profile"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &createdBody); err != nil {
		t.Fatalf("Unmarshal(created replica) error = %v", err)
	}
	if createdBody.StorageProfile != "aws" {
		t.Fatalf("createdBody.StorageProfile = %q, want aws", createdBody.StorageProfile)
	}

	var replica model.Replica
	if err := database.First(&replica, "node_id = ? AND inventory_id = ?", "node-b", inventory.ID).Error; err != nil {
		t.Fatalf("First(replica) error = %v", err)
	}
	if replica.StorageProfile != "aws" {
		t.Fatalf("replica.StorageProfile = %q, want aws", replica.StorageProfile)
	}

	var replicaFiles []model.ReplicaFile
	if err := database.Where("replica_id = ?", replica.ID).Order("file_id asc").Find(&replicaFiles).Error; err != nil {
		t.Fatalf("Find(replicaFiles) error = %v", err)
	}
	if len(replicaFiles) != len(files) {
		t.Fatalf("len(replicaFiles) = %d, want %d", len(replicaFiles), len(files))
	}
	for _, replicaFile := range replicaFiles {
		if replicaFile.Version != 0 || replicaFile.Status != model.ReplicaFileStatusPending {
			t.Fatalf("replicaFile = %+v, want version=0 status=pending", replicaFile)
		}
	}

	var command model.Command
	if err := database.First(&command, "node_id = ? AND type = ?", "node-b", model.NodeCommandTypeReconcileReplica).Error; err != nil {
		t.Fatalf("First(command) error = %v", err)
	}
	if command.Status != model.NodeCommandStatusPending {
		t.Fatalf("command.Status = %q, want %q", command.Status, model.NodeCommandStatusPending)
	}
	var payload service.ReconcileReplicaCommandPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal(command.Payload) error = %v", err)
	}
	if payload.DestinationReplicaID != replica.ID {
		t.Fatalf("payload.DestinationReplicaID = %d, want %d", payload.DestinationReplicaID, replica.ID)
	}
	if payload.SourceReplicaID != sourceReplica.ID || payload.SourceNodeID != "node-a" || payload.SourceNodeAddress != "https://node-a.example" || payload.TransferToken == "" {
		t.Fatalf("payload = %+v, want source replica/node/address and transfer token", payload)
	}
}

func TestInternalCommandsPatchUpdatesOwnedCommandStatus(t *testing.T) {
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
	pair, err := authService.NodeLogin("node-a", "node-secret", "")
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
		newRouterTestReplicaService(database, nil),
		service.NewShareService(repository.NewShareRepository(database), nil),
		nil,
	)

	req := httptest.NewRequest(http.MethodPatch, "/node/commands/"+strconv.FormatUint(uint64(command.ID), 10), strings.NewReader(`{"status":"failed","error":"refresh failed"}`))
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-API-Version", "1")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var body struct {
		ID        uint    `json:"id"`
		Status    string  `json:"status"`
		LastError *string `json:"last_error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if body.ID != command.ID {
		t.Fatalf("body.ID = %d, want %d", body.ID, command.ID)
	}
	if body.Status != string(model.NodeCommandStatusFailed) {
		t.Fatalf("body.Status = %q, want %q", body.Status, model.NodeCommandStatusFailed)
	}
	if body.LastError == nil || *body.LastError != "refresh failed" {
		t.Fatalf("body.LastError = %v, want refresh failed", body.LastError)
	}
}

func TestInternalReplicaFilesReportUpdatesCoordinatorState(t *testing.T) {
	database := openRouterTestDB(t)

	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	node := &model.Node{
		ID:      "node-a",
		Status:  model.NodeStatusOffline,
		Secret:  hashedSecret,
		Address: "https://node-a.example",
	}
	if err := database.Create(node).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-b", Status: model.NodeStatusOnline, Address: "https://node-b.example"}).Error; err != nil {
		t.Fatalf("Create(node-b) error = %v", err)
	}

	inventory := &model.Inventory{
		Name:   "photos",
		Status: model.InventoryStatusActive,
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
	pair, err := authService.NodeLogin("node-a", "node-secret", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}

	nodeService := service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	settingService := service.NewSettingService(repository.NewSettingRepository(database))
	if err := settingService.EnsureTransferKeys(); err != nil {
		t.Fatalf("EnsureTransferKeys() error = %v", err)
	}
	inventoryRepo := repository.NewInventoryRepository(database)
	handler := New(
		config.Config{},
		buildinfo.Info{Version: "test", Commit: "test", BuildDate: "test"},
		authService,
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		nodeService,
		service.NewInventoryService(inventoryRepo),
		service.NewReplicaService(repository.NewReplicaRepository(database), inventoryRepo, nodeService, settingService),
		service.NewShareService(repository.NewShareRepository(database), nil),
		nil,
	)

	req := httptest.NewRequest(http.MethodPost, "/node/replica/"+strconv.FormatUint(uint64(replicaA.ID), 10)+"/files", strings.NewReader(`{"files":[{"file_id":`+strconv.FormatUint(uint64(file.ID), 10)+`,"relative_uri":"album/img.jpg","file_size":200,"file_hash":"new-hash","created_time":"2026-05-21T11:00:00Z","modified_time":"2026-05-21T12:00:00Z"}]}`))
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

	var command model.Command
	if err := database.Where("node_id = ? AND type = ?", "node-b", model.NodeCommandTypeReconcileReplica).First(&command).Error; err != nil {
		t.Fatalf("First(command) error = %v", err)
	}
	var payload service.ReconcileReplicaCommandPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal(command.Payload) error = %v", err)
	}
	if payload.SourceReplicaID != replicaA.ID || payload.DestinationReplicaID != replicaB.ID || payload.TransferToken == "" {
		t.Fatalf("payload = %+v, want source=%d destination=%d with token", payload, replicaA.ID, replicaB.ID)
	}
}

func TestInternalReplicaFilePatchUpdatesStatus(t *testing.T) {
	database := openRouterTestDB(t)
	handler, accessToken, replica, file := newInternalReplicaFileStatusTestHandler(t, database)

	req := httptest.NewRequest(http.MethodPatch, "/node/replica/"+strconv.FormatUint(uint64(replica.ID), 10)+"/files/"+strconv.FormatUint(uint64(file.ID), 10), strings.NewReader(`{"status":"error","error":"copy failed"}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-API-Version", "1")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusNoContent, recorder.Body.String())
	}

	var replicaFile model.ReplicaFile
	if err := database.Where("replica_id = ? AND file_id = ?", replica.ID, file.ID).First(&replicaFile).Error; err != nil {
		t.Fatalf("First(replicaFile) error = %v", err)
	}
	if replicaFile.Status != model.ReplicaFileStatusError {
		t.Fatalf("replicaFile.Status = %q, want %q", replicaFile.Status, model.ReplicaFileStatusError)
	}
}

func TestInternalReplicaFilePatchRejectsInvalidStatus(t *testing.T) {
	database := openRouterTestDB(t)
	handler, accessToken, replica, file := newInternalReplicaFileStatusTestHandler(t, database)

	req := httptest.NewRequest(http.MethodPatch, "/node/replica/"+strconv.FormatUint(uint64(replica.ID), 10)+"/files/"+strconv.FormatUint(uint64(file.ID), 10), strings.NewReader(`{"status":"invalid"}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-API-Version", "1")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}

	var replicaFile model.ReplicaFile
	if err := database.Where("replica_id = ? AND file_id = ?", replica.ID, file.ID).First(&replicaFile).Error; err != nil {
		t.Fatalf("First(replicaFile) error = %v", err)
	}
	if replicaFile.Status != model.ReplicaFileStatusPending {
		t.Fatalf("replicaFile.Status = %q, want unchanged %q", replicaFile.Status, model.ReplicaFileStatusPending)
	}
}

func TestInternalReplicaFilePatchSynchronizesWithMatchingVersion(t *testing.T) {
	database := openRouterTestDB(t)
	handler, accessToken, replica, file := newInternalReplicaFileStatusTestHandler(t, database)

	req := httptest.NewRequest(http.MethodPatch, "/node/replica/"+strconv.FormatUint(uint64(replica.ID), 10)+"/files/"+strconv.FormatUint(uint64(file.ID), 10), strings.NewReader(`{"status":"synchronized","version":3}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-API-Version", "1")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusNoContent, recorder.Body.String())
	}

	var replicaFile model.ReplicaFile
	if err := database.Where("replica_id = ? AND file_id = ?", replica.ID, file.ID).First(&replicaFile).Error; err != nil {
		t.Fatalf("First(replicaFile) error = %v", err)
	}
	if replicaFile.Status != model.ReplicaFileStatusSynchronized || replicaFile.Version != 3 {
		t.Fatalf("replicaFile = %+v, want synchronized version=3", replicaFile)
	}
}

func TestInternalReplicaFilePatchSynchronizeRequiresVersion(t *testing.T) {
	database := openRouterTestDB(t)
	handler, accessToken, replica, file := newInternalReplicaFileStatusTestHandler(t, database)

	req := httptest.NewRequest(http.MethodPatch, "/node/replica/"+strconv.FormatUint(uint64(replica.ID), 10)+"/files/"+strconv.FormatUint(uint64(file.ID), 10), strings.NewReader(`{"status":"synchronized"}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-API-Version", "1")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}

func TestInternalReplicaFilePatchSynchronizeRejectsMismatchedVersion(t *testing.T) {
	database := openRouterTestDB(t)
	handler, accessToken, replica, file := newInternalReplicaFileStatusTestHandler(t, database)

	req := httptest.NewRequest(http.MethodPatch, "/node/replica/"+strconv.FormatUint(uint64(replica.ID), 10)+"/files/"+strconv.FormatUint(uint64(file.ID), 10), strings.NewReader(`{"status":"synchronized","version":2}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-API-Version", "1")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}

func newInternalReplicaFileStatusTestHandler(t *testing.T, database *gorm.DB) (http.Handler, string, model.Replica, model.InventoryFile) {
	t.Helper()

	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-a", Status: model.NodeStatusOffline, Secret: hashedSecret}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}

	inventory := model.Inventory{Name: "Photos", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(&inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}

	replica := model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-a",
		URI:         "/data/photos",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(&replica).Error; err != nil {
		t.Fatalf("Create(replica) error = %v", err)
	}

	file := model.InventoryFile{
		InventoryID: inventory.ID,
		RelativeURI: "album/img.jpg",
		Status:      model.InventoryFileStatusActive,
		Size:        100,
		Hash:        "hash",
		Version:     3,
		Created:     time.Date(2026, 5, 21, 11, 0, 0, 0, time.UTC),
		Modified:    time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC),
	}
	if err := database.Create(&file).Error; err != nil {
		t.Fatalf("Create(file) error = %v", err)
	}
	if err := database.Create(&model.ReplicaFile{
		FileID:    file.ID,
		ReplicaID: replica.ID,
		Version:   0,
		Status:    model.ReplicaFileStatusPending,
	}).Error; err != nil {
		t.Fatalf("Create(replicaFile) error = %v", err)
	}

	authService := newRouterTestAuthService(database)
	pair, err := authService.NodeLogin("node-a", "node-secret", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}

	nodeService := service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	inventoryRepo := repository.NewInventoryRepository(database)
	handler := New(
		config.Config{},
		buildinfo.Info{Version: "test", Commit: "test", BuildDate: "test"},
		authService,
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		nodeService,
		service.NewInventoryService(inventoryRepo),
		service.NewReplicaService(repository.NewReplicaRepository(database), inventoryRepo, nodeService),
		service.NewShareService(repository.NewShareRepository(database), nil),
		nil,
	)

	return handler, pair.AccessToken, replica, file
}

func newInternalReplicaFilesFilterTestHandler(t *testing.T, database *gorm.DB) (http.Handler, string, model.Replica, model.InventoryFile) {
	t.Helper()

	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-a", Status: model.NodeStatusOffline, Secret: hashedSecret}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}

	inventory := model.Inventory{Name: "Photos", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(&inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	replica := model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-a",
		URI:         "/data/photos",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(&replica).Error; err != nil {
		t.Fatalf("Create(replica) error = %v", err)
	}

	pendingFile := model.InventoryFile{
		InventoryID: inventory.ID,
		RelativeURI: "pending.txt",
		Status:      model.InventoryFileStatusActive,
		Size:        100,
		Hash:        "pending-hash",
		Version:     3,
		Created:     time.Date(2026, 5, 21, 11, 0, 0, 0, time.UTC),
		Modified:    time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC),
	}
	synchronizedFile := model.InventoryFile{
		InventoryID: inventory.ID,
		RelativeURI: "synchronized.txt",
		Status:      model.InventoryFileStatusActive,
		Size:        200,
		Hash:        "synchronized-hash",
		Version:     3,
		Created:     time.Date(2026, 5, 21, 11, 0, 0, 0, time.UTC),
		Modified:    time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC),
	}
	if err := database.Create(&pendingFile).Error; err != nil {
		t.Fatalf("Create(pendingFile) error = %v", err)
	}
	if err := database.Create(&synchronizedFile).Error; err != nil {
		t.Fatalf("Create(synchronizedFile) error = %v", err)
	}
	if err := database.Create(&[]model.ReplicaFile{
		{FileID: pendingFile.ID, ReplicaID: replica.ID, Version: 0, Status: model.ReplicaFileStatusPending},
		{FileID: synchronizedFile.ID, ReplicaID: replica.ID, Version: 3, Status: model.ReplicaFileStatusSynchronized},
	}).Error; err != nil {
		t.Fatalf("Create(replicaFiles) error = %v", err)
	}

	authService := newRouterTestAuthService(database)
	pair, err := authService.NodeLogin("node-a", "node-secret", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}

	nodeService := service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	inventoryRepo := repository.NewInventoryRepository(database)
	handler := New(
		config.Config{},
		buildinfo.Info{Version: "test", Commit: "test", BuildDate: "test"},
		authService,
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		nodeService,
		service.NewInventoryService(inventoryRepo),
		service.NewReplicaService(repository.NewReplicaRepository(database), inventoryRepo, nodeService),
		service.NewShareService(repository.NewShareRepository(database), nil),
		nil,
	)

	return handler, pair.AccessToken, replica, pendingFile
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

func waitForStoredNodeStatus(t *testing.T, database *gorm.DB, nodeID string, want model.NodeStatus) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var node model.Node
		if err := database.First(&node, "id = ?", nodeID).Error; err != nil {
			t.Fatalf("First(node) error = %v", err)
		}
		if node.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	var node model.Node
	if err := database.First(&node, "id = ?", nodeID).Error; err != nil {
		t.Fatalf("First(node) error = %v", err)
	}
	t.Fatalf("node.Status = %q, want %q", node.Status, want)
}

func newInternalAuthTestHandler(t *testing.T, database *gorm.DB) http.Handler {
	t.Helper()
	authService := newRouterTestAuthService(database)
	return New(
		config.Config{},
		buildinfo.Info{Version: "test", Commit: "test", BuildDate: "test"},
		authService,
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database)),
		service.NewInventoryService(repository.NewInventoryRepository(database)),
		nil,
		nil,
		nil,
	)
}

func createValidateUserTokenCredentials(t *testing.T, database *gorm.DB, userStatus model.UserStatus) (string, string) {
	t.Helper()

	hashedPassword, err := security.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword(user) error = %v", err)
	}
	user := &model.User{
		Name:     "token-user",
		Status:   userStatus,
		Password: hashedPassword,
	}
	if err := database.Create(user).Error; err != nil {
		t.Fatalf("Create(user) error = %v", err)
	}

	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword(node) error = %v", err)
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
	var userToken string
	if userStatus == model.UserStatusActive {
		pair, err := authService.Login("token-user", "secret")
		if err != nil {
			t.Fatalf("Login(user) error = %v", err)
		}
		userToken = pair.AccessToken
	} else {
		userToken, err = security.GenerateUserAccessToken([]byte("test-secret"), user.ID, 1, time.Now().UTC().Add(time.Hour))
		if err != nil {
			t.Fatalf("GenerateUserAccessToken() error = %v", err)
		}
	}
	nodePair, err := authService.NodeLogin("node-a", "node-secret", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}
	return userToken, nodePair.AccessToken
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

func newRouterTestReplicaService(database *gorm.DB, nodeService *service.NodeService) *service.ReplicaService {
	inventoryRepo := repository.NewInventoryRepository(database)
	if nodeService == nil {
		return service.NewReplicaService(repository.NewReplicaRepository(database), inventoryRepo)
	}
	return service.NewReplicaService(repository.NewReplicaRepository(database), inventoryRepo, nodeService)
}

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

	"dropoutbox/internal/buildinfo"
	"dropoutbox/internal/config"
	"dropoutbox/internal/db"
	"dropoutbox/internal/model"
	"dropoutbox/internal/repository"
	"dropoutbox/internal/security"
	"dropoutbox/internal/service"

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
	if err := database.Create(&model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-a",
		URI:         "/data/a",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}).Error; err != nil {
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

	authService := newRouterTestAuthService(database)
	pair, err := authService.NodeLogin("node-a", "node-secret")
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
	)

	req := httptest.NewRequest(http.MethodGet, "/internal/replicas", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var body []struct {
		NodeID string `json:"node_id"`
		URI    string `json:"uri"`
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
	for _, uri := range []string{"/data/a", "/data/b"} {
		if err := database.Create(&model.Replica{
			InventoryID: inventory.ID,
			NodeID:      "node-a",
			URI:         uri,
			Status:      model.ReplicaStatusActive,
			Type:        model.ReplicaTypeFilesystem,
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
	handler := New(
		config.Config{},
		buildinfo.Info{Version: "test", Commit: "test", BuildDate: "test"},
		authService,
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		nodeService,
		inventoryService,
	)

	req := httptest.NewRequest(http.MethodGet, "/api/replicas?page=1&count=1", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var body struct {
		Items []struct {
			URI string `json:"uri"`
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

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	wsURL.Scheme = "ws"
	wsURL.Path = "/internal/nodes/ws"

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+pair.AccessToken)

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL.String(), headers)
	if err != nil {
		if resp != nil {
			t.Fatalf("Dial() error = %v; status=%d", err, resp.StatusCode)
		}
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSwitchingProtocols)
	}
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
	nodePair, err := authService.NodeLogin("node-a", "node-secret")
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
	)

	server := httptest.NewServer(handler)
	defer server.Close()

	wsURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	wsURL.Scheme = "ws"
	wsURL.Path = "/internal/nodes/ws"

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

	createReq := httptest.NewRequest(http.MethodPost, "/api/inventories", strings.NewReader(`{"name":"Photos","type":"folder","node_id":"node-a","uri":"/data/photos"}`))
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

	reportReq := httptest.NewRequest(http.MethodPost, "/internal/nodes", strings.NewReader(`{"address":"https://node-address:8081"}`))
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
	if len(heartbeat.Commands) != 1 {
		t.Fatalf("len(heartbeat.Commands) = %d, want 1", len(heartbeat.Commands))
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

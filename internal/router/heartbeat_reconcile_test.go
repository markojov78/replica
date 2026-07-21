package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"replica/internal/buildinfo"
	"replica/internal/config"
	"replica/internal/model"
	"replica/internal/repository"
	"replica/internal/security"
	"replica/internal/service"
)

func TestHeartbeatCreatesMissingReconcileCommand(t *testing.T) {
	database := openRouterTestDB(t)
	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	nodes := []model.Node{
		{ID: "node-a", Status: model.NodeStatusOnline, Secret: hashedSecret, Address: "https://source.example"},
		{ID: "node-b", Status: model.NodeStatusOnline, Secret: hashedSecret, Address: "https://destination.example"},
	}
	if err := database.Create(&nodes).Error; err != nil {
		t.Fatalf("Create(nodes) error = %v", err)
	}
	inventory := model.Inventory{Name: "inventory", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(&inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	file := model.InventoryFile{InventoryID: inventory.ID, RelativeURI: "file.txt", Status: model.InventoryFileStatusActive, Version: 1}
	if err := database.Create(&file).Error; err != nil {
		t.Fatalf("Create(file) error = %v", err)
	}
	source := model.Replica{InventoryID: inventory.ID, NodeID: "node-a", URI: "/source", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem}
	destination := model.Replica{InventoryID: inventory.ID, NodeID: "node-b", URI: "/destination", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem}
	if err := database.Create(&source).Error; err != nil {
		t.Fatalf("Create(source) error = %v", err)
	}
	if err := database.Create(&destination).Error; err != nil {
		t.Fatalf("Create(destination) error = %v", err)
	}
	if err := database.Create(&[]model.ReplicaFile{
		{FileID: file.ID, ReplicaID: source.ID, Version: 1, Status: model.ReplicaFileStatusSynchronized},
		{FileID: file.ID, ReplicaID: destination.ID, Version: 0, Status: model.ReplicaFileStatusPending},
	}).Error; err != nil {
		t.Fatalf("Create(replica files) error = %v", err)
	}

	settings := service.NewSettingService(repository.NewSettingRepository(database))
	if err := settings.EnsureTransferKeys(); err != nil {
		t.Fatalf("EnsureTransferKeys() error = %v", err)
	}
	authService := newRouterTestAuthService(database)
	pair, err := authService.NodeLogin("node-b", "node-secret", "", "", "", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}
	nodeService := service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	inventoryRepo := repository.NewInventoryRepository(database)
	handler := New(
		config.Config{},
		buildinfo.Info{Version: "test"},
		authService,
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		nodeService,
		service.NewInventoryService(inventoryRepo, nodeService),
		service.NewReplicaService(repository.NewReplicaRepository(database), inventoryRepo, nodeService, settings),
		service.NewShareService(repository.NewShareRepository(database), nil),
		nil,
	)

	req := httptest.NewRequest(http.MethodPost, "/node/nodes", strings.NewReader(`{"address":"https://destination-current.example","interval":60}`))
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	req.Header.Set("X-API-Version", "1")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var report struct {
		Commands []service.NodeCommand `json:"commands"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &report); err != nil {
		t.Fatalf("Unmarshal(report) error = %v", err)
	}
	if len(report.Commands) != 1 || report.Commands[0].Type != string(model.NodeCommandTypeReconcileReplica) {
		t.Fatalf("commands = %+v, want one reconcile command", report.Commands)
	}
}

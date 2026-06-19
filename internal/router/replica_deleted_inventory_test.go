package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"replica/internal/buildinfo"
	"replica/internal/config"
	"replica/internal/model"
	"replica/internal/repository"
	"replica/internal/security"
	"replica/internal/service"
)

func TestPublicReplicaMutationsRejectDeletedInventory(t *testing.T) {
	database := openRouterTestDB(t)

	hashedPassword, err := security.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	user := &model.User{Name: "jsmith", Status: model.UserStatusActive, Password: hashedPassword}
	if err := database.Create(user).Error; err != nil {
		t.Fatalf("Create(user) error = %v", err)
	}
	role := &model.Role{Name: "inventory-updater", Status: model.RoleStatusActive}
	if err := database.Create(role).Error; err != nil {
		t.Fatalf("Create(role) error = %v", err)
	}
	if err := database.Create(&model.Permission{RoleID: role.ID, Resource: model.PermissionResourceInventories, Action: model.PermissionActionUpdate}).Error; err != nil {
		t.Fatalf("Create(permission) error = %v", err)
	}
	if err := database.Create(&model.Permission{RoleID: role.ID, Resource: model.PermissionResourceInventories, Action: model.PermissionActionDelete}).Error; err != nil {
		t.Fatalf("Create(delete permission) error = %v", err)
	}
	if err := database.Create(&model.UserRole{UserID: user.ID, RoleID: role.ID}).Error; err != nil {
		t.Fatalf("Create(user role) error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-a", Status: model.NodeStatusOffline, Secret: "ignored"}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	inventory := &model.Inventory{Name: "deleted", Status: model.InventoryStatusDeleted, Type: model.InventoryTypeFolder}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	replica := &model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-a",
		URI:         "/data/deleted",
		Status:      model.ReplicaStatusDeleted,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(replica).Error; err != nil {
		t.Fatalf("Create(replica) error = %v", err)
	}

	authService := newRouterTestAuthService(database)
	pair, err := authService.Login("jsmith", "secret")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	inventoryRepo := repository.NewInventoryRepository(database)
	handler := New(
		config.Config{},
		buildinfo.Info{Version: "test"},
		authService,
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database)),
		service.NewInventoryService(inventoryRepo),
		service.NewReplicaService(repository.NewReplicaRepository(database), inventoryRepo),
		service.NewShareService(repository.NewShareRepository(database), nil),
		nil,
	)

	createBody := `{"inventory_id":` + strconv.FormatUint(uint64(inventory.ID), 10) + `,"node_id":"node-a","uri":"/data/new","type":"filesystem"}`
	assertInventoryDeletedConflict(t, handler, pair.AccessToken, http.MethodPost, "/api/admin/replicas", createBody)
	assertInventoryDeletedConflict(t, handler, pair.AccessToken, http.MethodPatch, "/api/admin/replicas/"+strconv.FormatUint(uint64(replica.ID), 10), `{"status":"active"}`)

	activeInventory := &model.Inventory{Name: "active", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(activeInventory).Error; err != nil {
		t.Fatalf("Create(active inventory) error = %v", err)
	}
	activeReplica := &model.Replica{
		InventoryID: activeInventory.ID, NodeID: "node-a", URI: "/data/shared",
		Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem,
	}
	deletedReplica := &model.Replica{
		InventoryID: activeInventory.ID, NodeID: "node-a", URI: "/data/shared",
		Status: model.ReplicaStatusDeleted, Type: model.ReplicaTypeFilesystem,
	}
	if err := database.Create(activeReplica).Error; err != nil {
		t.Fatalf("Create(active replica) error = %v", err)
	}
	if err := database.Create(deletedReplica).Error; err != nil {
		t.Fatalf("Create(deleted replica) error = %v", err)
	}
	assertConflict(t, handler, pair.AccessToken, http.MethodDelete, "/api/admin/inventories/"+strconv.FormatUint(uint64(activeInventory.ID), 10), "", "inventory has active replicas")
	assertConflict(t, handler, pair.AccessToken, http.MethodPatch, "/api/admin/inventories/"+strconv.FormatUint(uint64(activeInventory.ID), 10), `{"status":"deleted"}`, "inventory has active replicas")

	sharedReplica := &model.Replica{
		InventoryID: activeInventory.ID, NodeID: "node-b", URI: "/data/shared-with-share",
		Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem,
	}
	if err := database.Create(sharedReplica).Error; err != nil {
		t.Fatalf("Create(shared replica) error = %v", err)
	}
	if err := database.Create(&model.Share{
		ReplicaID: sharedReplica.ID,
		Name:      "Shared",
		Status:    model.ShareStatusActive,
	}).Error; err != nil {
		t.Fatalf("Create(active share) error = %v", err)
	}
	assertConflict(t, handler, pair.AccessToken, http.MethodPatch, "/api/admin/replicas/"+strconv.FormatUint(uint64(sharedReplica.ID), 10), `{"status":"deleted"}`, "replica has active shares")
	assertConflict(t, handler, pair.AccessToken, http.MethodDelete, "/api/admin/replicas/"+strconv.FormatUint(uint64(sharedReplica.ID), 10), "", "replica has active shares")

	var storedInventory model.Inventory
	if err := database.First(&storedInventory, activeInventory.ID).Error; err != nil {
		t.Fatalf("First(active inventory) error = %v", err)
	}
	if storedInventory.Status != model.InventoryStatusActive {
		t.Fatalf("storedInventory.Status = %q, want %q", storedInventory.Status, model.InventoryStatusActive)
	}
	var replicaCount int64
	if err := database.Model(&model.Replica{}).Where("inventory_id = ?", activeInventory.ID).Count(&replicaCount).Error; err != nil {
		t.Fatalf("Count(replicas) error = %v", err)
	}
	if replicaCount != 3 {
		t.Fatalf("replicaCount = %d, want 3", replicaCount)
	}

	wantMessage := "Active replica " + strconv.FormatUint(uint64(activeReplica.ID), 10) + " on node-a is already using location /data/shared"
	assertConflict(t, handler, pair.AccessToken, http.MethodPatch, "/api/admin/replicas/"+strconv.FormatUint(uint64(deletedReplica.ID), 10), `{"status":"active"}`, wantMessage)
}

func assertInventoryDeletedConflict(t *testing.T, handler http.Handler, accessToken, method, path, body string) {
	t.Helper()
	assertConflict(t, handler, accessToken, method, path, body, "inventory is deleted")
}

func assertConflict(t *testing.T, handler http.Handler, accessToken, method, path, body, wantMessage string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-API-Version", "1")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("%s %s status = %d, want %d; body=%s", method, path, recorder.Code, http.StatusConflict, recorder.Body.String())
	}
	var problem struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("Unmarshal(problem) error = %v", err)
	}
	if problem.Detail != wantMessage {
		t.Fatalf("problem.Detail = %q, want %q", problem.Detail, wantMessage)
	}
}

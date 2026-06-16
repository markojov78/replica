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

	"gorm.io/gorm"
)

func TestShareRoutesCreateAndListWithGlobalPermissions(t *testing.T) {
	database := openRouterTestDB(t)
	user, accessToken := createShareRouteUser(t, database, []model.Permission{
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionCreate},
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionRead},
	})
	_ = user
	replica := createShareRouteReplica(t, database, model.ReplicaStatusActive)
	handler := newShareRouteHandler(database)

	req := httptest.NewRequest(http.MethodPost, "/api/shares", strings.NewReader(`{"replica_id":`+strconv.FormatUint(uint64(replica.ID), 10)+`,"name":"Vacation March 2026"}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var created service.ShareDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("Unmarshal(created) error = %v", err)
	}
	if created.InventoryID != replica.InventoryID || created.ReplicaID != replica.ID || created.Name != "Vacation March 2026" || created.Status != string(model.ShareStatusActive) {
		t.Fatalf("created = %+v, want inventory=%d replica=%d active", created, replica.InventoryID, replica.ID)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/shares?replica_id="+strconv.FormatUint(uint64(replica.ID), 10), nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var list service.ShareList
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatalf("Unmarshal(list) error = %v", err)
	}
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].ID != created.ID {
		t.Fatalf("list = %+v, want created share", list)
	}
}

func TestShareRouteGetAllowsExplicitSharePermission(t *testing.T) {
	database := openRouterTestDB(t)
	user, accessToken := createShareRouteUser(t, database, nil)
	replica := createShareRouteReplica(t, database, model.ReplicaStatusActive)
	share := model.Share{ReplicaID: replica.ID, Name: "Vacation March 2026", Status: model.ShareStatusActive}
	if err := database.Create(&share).Error; err != nil {
		t.Fatalf("Create(share) error = %v", err)
	}
	shareUser := model.ShareUser{UserID: user.ID, ShareID: share.ID}
	if err := database.Create(&shareUser).Error; err != nil {
		t.Fatalf("Create(share_user) error = %v", err)
	}
	if err := database.Create(&model.SharePermission{ShareUserID: shareUser.ID, Permission: string(model.PermissionActionRead)}).Error; err != nil {
		t.Fatalf("Create(share_permission) error = %v", err)
	}
	handler := newShareRouteHandler(database)

	req := httptest.NewRequest(http.MethodGet, "/api/shares/"+strconv.FormatUint(uint64(share.ID), 10), nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var body service.ShareDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal(body) error = %v", err)
	}
	if body.ID != share.ID || body.InventoryID != replica.InventoryID || body.ReplicaID != replica.ID {
		t.Fatalf("body = %+v, want share=%d inventory=%d replica=%d", body, share.ID, replica.InventoryID, replica.ID)
	}
}

func TestShareRouteListRequiresGlobalSharePermission(t *testing.T) {
	database := openRouterTestDB(t)
	_, accessToken := createShareRouteUser(t, database, nil)
	handler := newShareRouteHandler(database)

	req := httptest.NewRequest(http.MethodGet, "/api/shares", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}

func newShareRouteHandler(database *gorm.DB) http.Handler {
	return New(
		config.Config{},
		buildinfo.Info{Version: "test", Commit: "test", BuildDate: "test"},
		newRouterTestAuthService(database),
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database)),
		service.NewInventoryService(repository.NewInventoryRepository(database)),
		service.NewShareService(repository.NewShareRepository(database)),
	)
}

func createShareRouteUser(t *testing.T, database *gorm.DB, permissions []model.Permission) (*model.User, string) {
	t.Helper()

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
	if permissions != nil {
		role := &model.Role{Name: "share-role", Status: model.RoleStatusActive}
		if err := database.Create(role).Error; err != nil {
			t.Fatalf("Create(role) error = %v", err)
		}
		for i := range permissions {
			permissions[i].RoleID = role.ID
		}
		if len(permissions) > 0 {
			if err := database.Create(&permissions).Error; err != nil {
				t.Fatalf("Create(permissions) error = %v", err)
			}
		}
		if err := database.Create(&model.UserRole{UserID: user.ID, RoleID: role.ID}).Error; err != nil {
			t.Fatalf("Create(user_role) error = %v", err)
		}
	}

	authService := newRouterTestAuthService(database)
	pair, err := authService.Login("jsmith", "secret")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	return user, pair.AccessToken
}

func createShareRouteReplica(t *testing.T, database *gorm.DB, status model.ReplicaStatus) *model.Replica {
	t.Helper()

	inventory := &model.Inventory{Name: "Vacation March 2026", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	replica := &model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-a",
		URI:         "/data/photos",
		Status:      status,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(replica).Error; err != nil {
		t.Fatalf("Create(replica) error = %v", err)
	}
	return replica
}

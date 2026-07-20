package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestShareRoutesCreateAndListWithGlobalPermissions(t *testing.T) {
	database := openRouterTestDB(t)
	user, accessToken := createShareRouteUser(t, database, []model.Permission{
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionCreate},
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionRead},
	})
	replica := createShareRouteReplica(t, database, model.ReplicaStatusActive)
	handler := newShareRouteHandler(database)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/shares", strings.NewReader(`{"replica_id":`+strconv.FormatUint(uint64(replica.ID), 10)+`,"name":"Vacation March 2026","user_permissions":[{"user_id":`+strconv.FormatUint(uint64(user.ID), 10)+`,"permissions":["read","create","update","delete"]}],"anonymous_permissions":["read"]}`))
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
	if created.Properties.View != nil || created.Properties.PageSize != nil || created.Properties.ThumbnailSize != nil || created.Properties.Theme != nil {
		t.Fatalf("created.Properties = %+v, want null values", created.Properties)
	}
	if !strings.Contains(recorder.Body.String(), `"properties":{"view":null,"page_size":null,"thumbnail_size":null,"theme":null}`) {
		t.Fatalf("created response = %s, want explicit null property values", recorder.Body.String())
	}
	if len(created.UserPermissions) != 1 || created.UserPermissions[0].UserID != user.ID || len(created.UserPermissions[0].Permissions) != 4 {
		t.Fatalf("created.UserPermissions = %+v, want user %d with four permissions", created.UserPermissions, user.ID)
	}
	if len(created.AnonymousPermissions) != 1 || created.AnonymousPermissions[0] != string(model.PermissionActionRead) {
		t.Fatalf("created.AnonymousPermissions = %+v, want read", created.AnonymousPermissions)
	}
	var shareUser model.ShareUser
	if err := database.First(&shareUser, "user_id = ? AND share_id = ?", user.ID, created.ID).Error; err != nil {
		t.Fatalf("First(share_user) error = %v", err)
	}
	var permissions []model.SharePermission
	if err := database.Where("share_user_id = ?", shareUser.ID).Find(&permissions).Error; err != nil {
		t.Fatalf("Find(share_permissions) error = %v", err)
	}
	required := map[string]bool{
		string(model.PermissionActionRead):   false,
		string(model.PermissionActionCreate): false,
		string(model.PermissionActionUpdate): false,
		string(model.PermissionActionDelete): false,
	}
	for _, permission := range permissions {
		required[permission.Permission] = true
	}
	for permission, found := range required {
		if !found {
			t.Fatalf("share permission %q not granted; permissions=%+v", permission, permissions)
		}
	}
	var anonymousShareUser model.ShareUser
	if err := database.First(&anonymousShareUser, "share_id = ? AND anonymous = ?", created.ID, true).Error; err != nil {
		t.Fatalf("First(anonymous share_user) error = %v", err)
	}
	if anonymousShareUser.UserID != nil {
		t.Fatalf("anonymous share_user UserID = %v, want nil", *anonymousShareUser.UserID)
	}

	if err := database.Model(&model.Role{}).Where("name = ?", "share-role").Update("status", model.RoleStatusDeleted).Error; err != nil {
		t.Fatalf("Update(role status) error = %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/admin/shares/"+strconv.FormatUint(uint64(created.ID), 10), nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("explicit permission status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/admin/shares?replica_id="+strconv.FormatUint(uint64(replica.ID), 10), nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("list status = %d, want %d; body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
	if err := database.Model(&model.Role{}).Where("name = ?", "share-role").Update("status", model.RoleStatusActive).Error; err != nil {
		t.Fatalf("Reactivate(role) error = %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/admin/shares?replica_id="+strconv.FormatUint(uint64(replica.ID), 10), nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("reactivated list status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var list service.ShareList
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatalf("Unmarshal(list) error = %v", err)
	}
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].ID != created.ID {
		t.Fatalf("list = %+v, want created share", list)
	}
}

func TestShareRoutesValidatePaginationWithoutUpperLimit(t *testing.T) {
	database := openRouterTestDB(t)
	_, accessToken := createShareRouteUser(t, database, []model.Permission{
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionRead},
	})
	handler := newShareRouteHandler(database)

	for _, query := range []string{"page=0", "page=-1", "count=0", "count=-1"} {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/shares?"+query, nil)
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("X-API-Version", "1")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want %d; body=%s", query, recorder.Code, http.StatusBadRequest, recorder.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/shares?count=101", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("count=101 status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var list service.ShareList
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatalf("Unmarshal(list) error = %v", err)
	}
	if list.Count != 101 {
		t.Fatalf("list.Count = %d, want 101", list.Count)
	}
}

func TestShareRoutesFilterByInventoryAndNode(t *testing.T) {
	database := openRouterTestDB(t)
	_, accessToken := createShareRouteUser(t, database, []model.Permission{
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionRead},
	})
	firstReplica := createShareRouteReplica(t, database, model.ReplicaStatusActive)
	secondReplica := createShareRouteReplica(t, database, model.ReplicaStatusActive)
	if err := database.Model(secondReplica).Update("node_id", "node-b").Error; err != nil {
		t.Fatalf("Update(replica node) error = %v", err)
	}

	firstShare := &model.Share{ReplicaID: firstReplica.ID, Name: "First", Status: model.ShareStatusActive}
	secondShare := &model.Share{ReplicaID: secondReplica.ID, Name: "Second", Status: model.ShareStatusActive}
	if err := database.Create(firstShare).Error; err != nil {
		t.Fatalf("Create(first share) error = %v", err)
	}
	if err := database.Create(secondShare).Error; err != nil {
		t.Fatalf("Create(second share) error = %v", err)
	}

	handler := newShareRouteHandler(database)
	tests := []struct {
		name    string
		query   string
		wantID  uint
		wantLen int
	}{
		{name: "inventory", query: "inventory_id=" + strconv.FormatUint(uint64(firstReplica.InventoryID), 10), wantID: firstShare.ID, wantLen: 1},
		{name: "node", query: "node_id=node-b", wantID: secondShare.ID, wantLen: 1},
		{name: "combined mismatch", query: "inventory_id=" + strconv.FormatUint(uint64(firstReplica.InventoryID), 10) + "&node_id=node-b", wantLen: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/admin/shares?"+tt.query, nil)
			req.Header.Set("Authorization", "Bearer "+accessToken)
			req.Header.Set("X-API-Version", "1")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			var list service.ShareList
			if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
				t.Fatalf("Unmarshal(list) error = %v", err)
			}
			if list.Total != int64(tt.wantLen) || len(list.Items) != tt.wantLen {
				t.Fatalf("list = %+v, want %d item(s)", list, tt.wantLen)
			}
			if tt.wantLen > 0 && list.Items[0].ID != tt.wantID {
				t.Fatalf("item ID = %d, want %d", list.Items[0].ID, tt.wantID)
			}
		})
	}
}

func TestShareRoutesCreatePatchAndValidateProperties(t *testing.T) {
	database := openRouterTestDB(t)
	_, accessToken := createShareRouteUser(t, database, []model.Permission{
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionCreate},
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionRead},
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionUpdate},
	})
	replica := createShareRouteReplica(t, database, model.ReplicaStatusActive)
	handler := newShareRouteHandler(database)

	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Version", "1")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder
	}
	invalidProperties := []string{
		`{"view":"table"}`,
		`{"count":100}`,
		`{"page_size":0}`,
		`{"page_size":1.5}`,
		`{"thumbnail_size":999}`,
		`{"theme":"system"}`,
		`{"unknown":true}`,
	}
	for _, properties := range invalidProperties {
		recorder := request(http.MethodPost, "/api/admin/shares", `{"replica_id":`+strconv.FormatUint(uint64(replica.ID), 10)+`,"properties":`+properties+`}`)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("POST properties %s status = %d, want %d; body=%s", properties, recorder.Code, http.StatusBadRequest, recorder.Body.String())
		}
	}

	recorder := request(http.MethodPost, "/api/admin/shares", `{"replica_id":`+strconv.FormatUint(uint64(replica.ID), 10)+`,"properties":{"view":"grid","page_size":100,"thumbnail_size":256,"theme":"dark"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var created service.ShareDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("Unmarshal(created) error = %v", err)
	}
	if created.Properties.View == nil || *created.Properties.View != "grid" ||
		created.Properties.PageSize == nil || *created.Properties.PageSize != 100 ||
		created.Properties.ThumbnailSize == nil || *created.Properties.ThumbnailSize != 256 ||
		created.Properties.Theme == nil || *created.Properties.Theme != "dark" {
		t.Fatalf("created.Properties = %+v, want all requested values", created.Properties)
	}

	sharePath := "/api/admin/shares/" + strconv.FormatUint(uint64(created.ID), 10)
	recorder = request(http.MethodPatch, sharePath, `{"properties":{"view":null,"page_size":25,"theme":"light"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var patched service.ShareDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &patched); err != nil {
		t.Fatalf("Unmarshal(patched) error = %v", err)
	}
	if patched.Properties.View != nil || patched.Properties.PageSize == nil || *patched.Properties.PageSize != 25 ||
		patched.Properties.ThumbnailSize == nil || *patched.Properties.ThumbnailSize != 256 ||
		patched.Properties.Theme == nil || *patched.Properties.Theme != "light" {
		t.Fatalf("patched.Properties = %+v, want merged values with view cleared", patched.Properties)
	}

	for _, properties := range invalidProperties {
		recorder = request(http.MethodPatch, sharePath, `{"properties":`+properties+`}`)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("PATCH properties %s status = %d, want %d; body=%s", properties, recorder.Code, http.StatusBadRequest, recorder.Body.String())
		}
	}

	recorder = request(http.MethodGet, sharePath, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &patched); err != nil {
		t.Fatalf("Unmarshal(get) error = %v", err)
	}
	if patched.Properties.View != nil || patched.Properties.PageSize == nil || *patched.Properties.PageSize != 25 ||
		patched.Properties.ThumbnailSize == nil || *patched.Properties.ThumbnailSize != 256 ||
		patched.Properties.Theme == nil || *patched.Properties.Theme != "light" {
		t.Fatalf("properties after invalid patches = %+v, want previous valid values", patched.Properties)
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
	userID := user.ID
	shareUser := model.ShareUser{UserID: &userID, ShareID: share.ID}
	if err := database.Create(&shareUser).Error; err != nil {
		t.Fatalf("Create(share_user) error = %v", err)
	}
	if err := database.Create(&model.SharePermission{ShareUserID: shareUser.ID, Permission: string(model.PermissionActionRead)}).Error; err != nil {
		t.Fatalf("Create(share_permission) error = %v", err)
	}
	handler := newShareRouteHandler(database)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/shares/"+strconv.FormatUint(uint64(share.ID), 10), nil)
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

	req := httptest.NewRequest(http.MethodGet, "/api/admin/shares", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}

func TestShareRouteMutationsCreateRefreshStateCommands(t *testing.T) {
	database := openRouterTestDB(t)
	_, accessToken := createShareRouteUser(t, database, []model.Permission{
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionCreate},
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionUpdate},
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionDelete},
	})
	replica := createShareRouteReplica(t, database, model.ReplicaStatusActive)
	handler := newShareRouteHandler(database)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/shares", strings.NewReader(`{"replica_id":`+strconv.FormatUint(uint64(replica.ID), 10)+`,"name":"Vacation March 2026"}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var created service.ShareDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("Unmarshal(created) error = %v", err)
	}
	assertShareRefreshCommands(t, database, replica.NodeID, 1)

	req = httptest.NewRequest(http.MethodPatch, "/api/admin/shares/"+strconv.FormatUint(uint64(created.ID), 10), strings.NewReader(`{"name":"Vacation renamed"}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	assertShareRefreshCommands(t, database, replica.NodeID, 2)

	req = httptest.NewRequest(http.MethodDelete, "/api/admin/shares/"+strconv.FormatUint(uint64(created.ID), 10), nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d; body=%s", recorder.Code, http.StatusNoContent, recorder.Body.String())
	}
	assertShareRefreshCommands(t, database, replica.NodeID, 3)
}

func TestShareRouteUserPermissionsOmittedAndPatchReplacement(t *testing.T) {
	database := openRouterTestDB(t)
	creator, accessToken := createShareRouteUser(t, database, []model.Permission{
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionCreate},
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionUpdate},
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionRead},
	})
	other := createShareRoutePlainUser(t, database, "other")
	replica := createShareRouteReplica(t, database, model.ReplicaStatusActive)
	handler := newShareRouteHandler(database)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/shares", strings.NewReader(`{"replica_id":`+strconv.FormatUint(uint64(replica.ID), 10)+`,"name":"Vacation March 2026"}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var created service.ShareDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("Unmarshal(created) error = %v", err)
	}
	if len(created.UserPermissions) != 0 {
		t.Fatalf("created.UserPermissions = %+v, want none when omitted", created.UserPermissions)
	}
	var shareUserCount int64
	if err := database.Model(&model.ShareUser{}).Where("share_id = ? AND user_id = ?", created.ID, creator.ID).Count(&shareUserCount).Error; err != nil {
		t.Fatalf("Count(creator share_user) error = %v", err)
	}
	if shareUserCount != 0 {
		t.Fatalf("creator share_user count = %d, want 0", shareUserCount)
	}

	req = httptest.NewRequest(http.MethodPatch, "/api/admin/shares/"+strconv.FormatUint(uint64(created.ID), 10), strings.NewReader(`{"user_permissions":[{"user_id":`+strconv.FormatUint(uint64(other.ID), 10)+`,"permissions":["read","update"]}]}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("patch set status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var patched service.ShareDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &patched); err != nil {
		t.Fatalf("Unmarshal(patched) error = %v", err)
	}
	if len(patched.UserPermissions) != 1 || patched.UserPermissions[0].UserID != other.ID || len(patched.UserPermissions[0].Permissions) != 2 {
		t.Fatalf("patched.UserPermissions = %+v, want other read/update", patched.UserPermissions)
	}

	req = httptest.NewRequest(http.MethodPatch, "/api/admin/shares/"+strconv.FormatUint(uint64(created.ID), 10), strings.NewReader(`{"name":"Vacation renamed"}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("patch omitted status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &patched); err != nil {
		t.Fatalf("Unmarshal(patched omitted) error = %v", err)
	}
	if len(patched.UserPermissions) != 1 || patched.UserPermissions[0].UserID != other.ID {
		t.Fatalf("patched omitted UserPermissions = %+v, want unchanged", patched.UserPermissions)
	}

	req = httptest.NewRequest(http.MethodPatch, "/api/admin/shares/"+strconv.FormatUint(uint64(created.ID), 10), strings.NewReader(`{"user_permissions":[]}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("patch clear status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &patched); err != nil {
		t.Fatalf("Unmarshal(patched clear) error = %v", err)
	}
	if len(patched.UserPermissions) != 0 {
		t.Fatalf("patched clear UserPermissions = %+v, want none", patched.UserPermissions)
	}
}

func TestShareRouteAnonymousPermissionsCreateAndPatchReplacement(t *testing.T) {
	database := openRouterTestDB(t)
	_, accessToken := createShareRouteUser(t, database, []model.Permission{
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionCreate},
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionUpdate},
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionRead},
	})
	replica := createShareRouteReplica(t, database, model.ReplicaStatusActive)
	handler := newShareRouteHandler(database)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/shares", strings.NewReader(`{"replica_id":`+strconv.FormatUint(uint64(replica.ID), 10)+`,"anonymous_permissions":["read","read","update"]}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var created service.ShareDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("Unmarshal(created) error = %v", err)
	}
	if len(created.AnonymousPermissions) != 2 || created.AnonymousPermissions[0] != string(model.PermissionActionRead) || created.AnonymousPermissions[1] != string(model.PermissionActionUpdate) {
		t.Fatalf("created.AnonymousPermissions = %+v, want read/update", created.AnonymousPermissions)
	}

	req = httptest.NewRequest(http.MethodPatch, "/api/admin/shares/"+strconv.FormatUint(uint64(created.ID), 10), strings.NewReader(`{"name":"Vacation renamed"}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("patch omitted status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var patched service.ShareDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &patched); err != nil {
		t.Fatalf("Unmarshal(patched omitted) error = %v", err)
	}
	if len(patched.AnonymousPermissions) != 2 {
		t.Fatalf("patched omitted AnonymousPermissions = %+v, want unchanged", patched.AnonymousPermissions)
	}

	req = httptest.NewRequest(http.MethodPatch, "/api/admin/shares/"+strconv.FormatUint(uint64(created.ID), 10), strings.NewReader(`{"anonymous_permissions":["delete"]}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("patch replace status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &patched); err != nil {
		t.Fatalf("Unmarshal(patched replace) error = %v", err)
	}
	if len(patched.AnonymousPermissions) != 1 || patched.AnonymousPermissions[0] != string(model.PermissionActionDelete) {
		t.Fatalf("patched replace AnonymousPermissions = %+v, want delete", patched.AnonymousPermissions)
	}

	req = httptest.NewRequest(http.MethodPatch, "/api/admin/shares/"+strconv.FormatUint(uint64(created.ID), 10), strings.NewReader(`{"anonymous_permissions":[]}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("patch clear status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &patched); err != nil {
		t.Fatalf("Unmarshal(patched clear) error = %v", err)
	}
	if len(patched.AnonymousPermissions) != 0 {
		t.Fatalf("patched clear AnonymousPermissions = %+v, want none", patched.AnonymousPermissions)
	}
	var anonymousCount int64
	if err := database.Model(&model.ShareUser{}).Where("share_id = ? AND anonymous = ?", created.ID, true).Count(&anonymousCount).Error; err != nil {
		t.Fatalf("Count(anonymous share_users) error = %v", err)
	}
	if anonymousCount != 0 {
		t.Fatalf("anonymous share_user count = %d, want 0", anonymousCount)
	}
}

func TestShareRouteLinkHashAndExpirationCreatePatchBehavior(t *testing.T) {
	database := openRouterTestDB(t)
	_, accessToken := createShareRouteUser(t, database, []model.Permission{
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionCreate},
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionUpdate},
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionRead},
	})
	replica := createShareRouteReplica(t, database, model.ReplicaStatusActive)
	handler := newShareRouteHandler(database)
	expiresAt := time.Date(2026, 3, 17, 10, 30, 0, 0, time.UTC)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/shares", strings.NewReader(`{"replica_id":`+strconv.FormatUint(uint64(replica.ID), 10)+`,"share_expiration":`+strconv.Quote(expiresAt.Format(time.RFC3339))+`,"generate_hash":true}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var created service.ShareDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("Unmarshal(created) error = %v", err)
	}
	if created.LinkHash == nil || *created.LinkHash == "" {
		t.Fatalf("created.LinkHash = %v, want generated value", created.LinkHash)
	}
	if created.ShareExpiration == nil || !created.ShareExpiration.Equal(expiresAt) {
		t.Fatalf("created.ShareExpiration = %v, want %v", created.ShareExpiration, expiresAt)
	}
	firstHash := *created.LinkHash

	req = httptest.NewRequest(http.MethodPatch, "/api/admin/shares/"+strconv.FormatUint(uint64(created.ID), 10), strings.NewReader(`{"name":"Vacation renamed"}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("patch omitted status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var patched service.ShareDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &patched); err != nil {
		t.Fatalf("Unmarshal(patched omitted) error = %v", err)
	}
	if patched.LinkHash == nil || *patched.LinkHash != firstHash {
		t.Fatalf("patched.LinkHash = %v, want unchanged %q", patched.LinkHash, firstHash)
	}
	if patched.ShareExpiration == nil || !patched.ShareExpiration.Equal(expiresAt) {
		t.Fatalf("patched.ShareExpiration = %v, want unchanged %v", patched.ShareExpiration, expiresAt)
	}

	req = httptest.NewRequest(http.MethodPatch, "/api/admin/shares/"+strconv.FormatUint(uint64(created.ID), 10), strings.NewReader(`{"generate_hash":false,"share_expiration":null}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("patch clear status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &patched); err != nil {
		t.Fatalf("Unmarshal(patched clear) error = %v", err)
	}
	if patched.LinkHash != nil {
		t.Fatalf("patched.LinkHash = %v, want nil", *patched.LinkHash)
	}
	if patched.ShareExpiration != nil {
		t.Fatalf("patched.ShareExpiration = %v, want nil", patched.ShareExpiration)
	}

	req = httptest.NewRequest(http.MethodPatch, "/api/admin/shares/"+strconv.FormatUint(uint64(created.ID), 10), strings.NewReader(`{"generate_hash":true,"share_expiration":""}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("patch regenerate status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &patched); err != nil {
		t.Fatalf("Unmarshal(patched regenerate) error = %v", err)
	}
	if patched.LinkHash == nil || *patched.LinkHash == "" || *patched.LinkHash == firstHash {
		t.Fatalf("patched.LinkHash = %v, want new generated value", patched.LinkHash)
	}
	if patched.ShareExpiration != nil {
		t.Fatalf("patched.ShareExpiration = %v, want nil", patched.ShareExpiration)
	}
}

func TestShareRouteInvalidExpiration(t *testing.T) {
	database := openRouterTestDB(t)
	_, accessToken := createShareRouteUser(t, database, []model.Permission{
		{Resource: model.PermissionResourceShares, Action: model.PermissionActionCreate},
	})
	replica := createShareRouteReplica(t, database, model.ReplicaStatusActive)
	handler := newShareRouteHandler(database)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/shares", strings.NewReader(`{"replica_id":`+strconv.FormatUint(uint64(replica.ID), 10)+`,"share_expiration":"not-a-time"}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}

func TestInventoryRouteUserPermissionsCreateAndPatchReplacement(t *testing.T) {
	database := openRouterTestDB(t)
	_, accessToken := createShareRouteUser(t, database, []model.Permission{
		{Resource: model.PermissionResourceInventories, Action: model.PermissionActionCreate},
		{Resource: model.PermissionResourceInventories, Action: model.PermissionActionUpdate},
		{Resource: model.PermissionResourceInventories, Action: model.PermissionActionRead},
	})
	other := createShareRoutePlainUser(t, database, "other")
	if err := database.Create(&model.Node{ID: "node-a", Status: model.NodeStatusOffline, Secret: "ignored"}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	handler := newShareRouteHandler(database)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/inventories", strings.NewReader(`{"name":"Photos","node_id":"node-a","folder_uri":"/data/photos","replica_type":"filesystem","user_permissions":[{"user_id":`+strconv.FormatUint(uint64(other.ID), 10)+`,"permissions":["read"]}]}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("create inventory status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var created service.InventoryDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("Unmarshal(created inventory) error = %v", err)
	}
	if len(created.UserPermissions) != 1 || created.UserPermissions[0].UserID != other.ID || len(created.UserPermissions[0].Permissions) != 1 {
		t.Fatalf("created.UserPermissions = %+v, want other read", created.UserPermissions)
	}

	req = httptest.NewRequest(http.MethodPatch, "/api/admin/inventories/"+strconv.FormatUint(uint64(created.ID), 10), strings.NewReader(`{"user_permissions":[]}`))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Version", "1")
	recorder = httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("patch inventory clear status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var patched service.InventoryDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &patched); err != nil {
		t.Fatalf("Unmarshal(patched inventory) error = %v", err)
	}
	if len(patched.UserPermissions) != 0 {
		t.Fatalf("patched.UserPermissions = %+v, want none", patched.UserPermissions)
	}
}

func newShareRouteHandler(database *gorm.DB) http.Handler {
	nodeService := service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	sharingConfig := config.SharingConfig{ThumbnailSizes: []int{128, 256, 512}}
	return New(
		config.Config{Sharing: sharingConfig},
		buildinfo.Info{Version: "test", Commit: "test", BuildDate: "test"},
		newRouterTestAuthService(database),
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		nodeService,
		service.NewInventoryService(repository.NewInventoryRepository(database)),
		nil,
		service.NewShareService(repository.NewShareRepository(database), nodeService, func() config.SharingConfig { return sharingConfig }),
		nil,
	)
}

func assertShareRefreshCommands(t *testing.T, database *gorm.DB, nodeID string, want int64) {
	t.Helper()

	var count int64
	if err := database.Model(&model.Command{}).
		Where("node_id = ? AND type = ? AND status = ?", nodeID, model.NodeCommandTypeRefreshState, model.NodeCommandStatusPending).
		Count(&count).Error; err != nil {
		t.Fatalf("Count(refresh_state commands) error = %v", err)
	}
	if count != want {
		t.Fatalf("refresh_state command count = %d, want %d", count, want)
	}

	var latest model.Command
	if err := database.Where("node_id = ? AND type = ?", nodeID, model.NodeCommandTypeRefreshState).Order("id desc").First(&latest).Error; err != nil {
		t.Fatalf("First(latest refresh_state command) error = %v", err)
	}
	if string(latest.Payload) != "{}" {
		t.Fatalf("latest refresh_state payload = %s, want {}", string(latest.Payload))
	}
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

func createShareRoutePlainUser(t *testing.T, database *gorm.DB, name string) *model.User {
	t.Helper()

	hashedPassword, err := security.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	user := &model.User{
		Name:     name,
		Status:   model.UserStatusActive,
		Password: hashedPassword,
	}
	if err := database.Create(user).Error; err != nil {
		t.Fatalf("Create(plain user) error = %v", err)
	}
	return user
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

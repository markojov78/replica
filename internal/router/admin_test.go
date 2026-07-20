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
	"replica/internal/seed"
	"replica/internal/service"
)

func TestAdminUIRequiresLoginAndManagesInventory(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "admin-ui.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}
	if err := seed.Run(database, config.SeedConfig{AdminName: "admin", AdminPassword: "secret"}); err != nil {
		t.Fatalf("seed.Run() error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-a", Status: model.NodeStatusOffline, Secret: "ignored", Sharing: true}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-b", Status: model.NodeStatusOffline, Secret: "ignored"}).Error; err != nil {
		t.Fatalf("Create(node-b) error = %v", err)
	}

	nodeRepo := repository.NewNodeRepository(database)
	commandRepo := repository.NewNodeCommandRepository(database)
	inventoryRepo := repository.NewInventoryRepository(database)
	nodeService := service.NewNodeService(nodeRepo, commandRepo)
	settingService := service.NewSettingService(repository.NewSettingRepository(database))
	configService := service.NewConfigService(repository.NewConfigRepository(database), config.Config{
		Sharing: config.SharingConfig{
			ThumbnailSizes:             []int{128, 256, 512},
			ThumbnailDefaultSize:       256,
			ThumbnailsGenerateForVideo: true,
			VideoInlineMaxSizeMB:       25,
			VideoPlaybackEnabled:       true,
		},
	})
	handler := New(
		config.Config{
			App: config.AppConfig{
				Coordinator:       true,
				CoordinatorURL:    "http://coordinator.test:8080",
				HeartbeatInterval: 60 * time.Second,
			},
			HTTP: config.HTTPConfig{Address: ":8080"},
			Storage: config.StorageConfig{Profiles: map[string]config.StorageProfileConfig{
				"aws":       {},
				"backblaze": {},
			}},
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
		),
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		nodeService,
		service.NewInventoryService(inventoryRepo, nodeService),
		service.NewReplicaService(repository.NewReplicaRepository(database), inventoryRepo, nodeService, settingService),
		service.NewShareService(repository.NewShareRepository(database), nil, func() config.SharingConfig {
			return configService.EffectiveConfig().Sharing
		}),
		nil,
		configService,
	)

	response := adminRequest(t, handler, http.MethodGet, "/dashboard/nodes", nil, "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "data-login-form") {
		t.Fatalf("protected response = %d body=%q, want login page", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/static/admin.js", nil, "")
	for _, required := range []string{
		"localStorage",
		"access_token_expires_at",
		"refresh_token_expires_at",
		"/api/admin/auth/login",
		"/api/admin/auth/refresh",
		"/api/admin/auth/logout",
		"/api/admin/auth/me",
		"data-hide-deleted",
		"replica_admin_user_",
		"replica_admin_username",
	} {
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), required) {
			t.Fatalf("admin.js response = %d, missing %q", response.Code, required)
		}
	}

	loginBody, err := json.Marshal(map[string]string{"username": "admin", "password": "secret"})
	if err != nil {
		t.Fatalf("json.Marshal(login) error = %v", err)
	}
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/admin/auth/login", bytes.NewReader(loginBody))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginRequest.Header.Set("X-API-Version", "1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, loginRequest)
	if response.Code != http.StatusOK {
		t.Fatalf("login response = %d body=%q", response.Code, response.Body.String())
	}
	var pair struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&pair); err != nil {
		t.Fatalf("decode login response error = %v", err)
	}
	accessToken := pair.AccessToken
	var adminUser model.User
	if err := database.First(&adminUser, "name = ?", "admin").Error; err != nil {
		t.Fatalf("First(admin user) error = %v", err)
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/users", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `href="/dashboard/users"`) ||
		!strings.Contains(response.Body.String(), `data-current-username`) ||
		!strings.Contains(response.Body.String(), `data-hide-deleted="users"`) ||
		!strings.Contains(response.Body.String(), "admin") {
		t.Fatalf("users response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/users/new", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "New user") ||
		!strings.Contains(response.Body.String(), `name="role_ids"`) ||
		!strings.Contains(response.Body.String(), "Admin") {
		t.Fatalf("new user response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/dashboard/users", url.Values{
		"name":     {"operator"},
		"password": {"operator-secret"},
		"role_ids": {"1"},
	}, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard/users" {
		t.Fatalf("create user response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/users/2/edit", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "Edit user") ||
		!strings.Contains(response.Body.String(), `value="operator"`) ||
		!strings.Contains(response.Body.String(), `value="1" selected`) {
		t.Fatalf("edit user response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/dashboard/users/2", url.Values{
		"name":     {"operator-updated"},
		"password": {""},
		"status":   {"deleted"},
		"role_ids": {"1"},
	}, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard/users" {
		t.Fatalf("update user response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/users", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "operator-updated") ||
		!strings.Contains(response.Body.String(), `data-filter-item="users" data-status="deleted"`) {
		t.Fatalf("updated users response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/roles", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `href="/dashboard/roles"`) ||
		!strings.Contains(response.Body.String(), "Admin") ||
		!strings.Contains(response.Body.String(), `data-hide-deleted="roles"`) ||
		!strings.Contains(response.Body.String(), `data-filter-item="roles"`) {
		t.Fatalf("roles response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/roles/new", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "New role") ||
		!strings.Contains(response.Body.String(), `value="users:read"`) ||
		!strings.Contains(response.Body.String(), `value="shares:update"`) ||
		!strings.Contains(response.Body.String(), `value="inventories:create"`) ||
		!strings.Contains(response.Body.String(), `value="nodes:delete"`) ||
		!strings.Contains(response.Body.String(), `value="settings:read"`) ||
		!strings.Contains(response.Body.String(), `value="settings:update"`) ||
		strings.Contains(response.Body.String(), `value="settings:create"`) ||
		strings.Contains(response.Body.String(), `value="settings:delete"`) ||
		strings.Contains(response.Body.String(), `name="status"`) {
		t.Fatalf("new role response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/dashboard/roles", url.Values{
		"name":        {"operators"},
		"description": {"Operations team"},
		"permissions": {"users:read", "inventories:read", "nodes:update", "settings:read"},
	}, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard/roles" {
		t.Fatalf("create role response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/roles/2/edit", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "Edit role") ||
		!strings.Contains(response.Body.String(), `value="operators"`) ||
		!strings.Contains(response.Body.String(), `name="status"`) ||
		!strings.Contains(response.Body.String(), `value="users:read" checked`) ||
		!strings.Contains(response.Body.String(), `value="nodes:update" checked`) ||
		!strings.Contains(response.Body.String(), `value="settings:read" checked`) {
		t.Fatalf("edit role response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/dashboard/roles/2", url.Values{
		"name":        {"operators-updated"},
		"description": {"Updated operations team"},
		"status":      {"deleted"},
		"permissions": {"shares:read", "nodes:delete", "settings:update"},
	}, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard/roles" {
		t.Fatalf("update role response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/roles", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "operators-updated") ||
		!strings.Contains(response.Body.String(), "shares: read") ||
		!strings.Contains(response.Body.String(), "nodes: delete") ||
		!strings.Contains(response.Body.String(), "settings: update") ||
		!strings.Contains(response.Body.String(), `data-filter-item="roles" data-status="deleted"`) {
		t.Fatalf("updated roles response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/inventories", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "Inventories") ||
		!strings.Contains(response.Body.String(), `data-hide-deleted="inventories"`) {
		t.Fatalf("inventories response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/inventories/new", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `name="folder_uri"`) ||
		!strings.Contains(response.Body.String(), `name="file_uris"`) ||
		!strings.Contains(response.Body.String(), `name="replica_type"`) ||
		!strings.Contains(response.Body.String(), `name="storage_profile"`) ||
		!strings.Contains(response.Body.String(), `name="follow_symlinks"`) ||
		!strings.Contains(response.Body.String(), `name="user_permissions_`+strconv.FormatUint(uint64(adminUser.ID), 10)+`"`) ||
		strings.Contains(response.Body.String(), `name="user_permissions_2"`) {
		t.Fatalf("new inventory form response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/dashboard/inventories", url.Values{
		"name":         {"Documents"},
		"node_id":      {"node-a"},
		"folder_uri":   {"/srv/documents"},
		"replica_type": {"filesystem"},
		"user_permissions_" + strconv.FormatUint(uint64(adminUser.ID), 10): {"read", "update"},
	}, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard/inventories/1" {
		t.Fatalf("create inventory response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	var inventoryUser model.InventoryUser
	if err := database.First(&inventoryUser, "inventory_id = ? AND user_id = ?", 1, adminUser.ID).Error; err != nil {
		t.Fatalf("First(inventory user) error = %v", err)
	}
	var inventoryPermissionCount int64
	if err := database.Model(&model.InventoryPermission{}).Where("inventory_user_id = ?", inventoryUser.ID).Count(&inventoryPermissionCount).Error; err != nil {
		t.Fatalf("Count(inventory permissions) error = %v", err)
	}
	if inventoryPermissionCount != 2 {
		t.Fatalf("inventoryPermissionCount = %d, want 2", inventoryPermissionCount)
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/inventories", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `data-filter-item="inventories"`) ||
		!strings.Contains(response.Body.String(), `folder · Inventory #1 · 1 replicas - 0 shares`) {
		t.Fatalf("inventories filtering markup response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/inventories/1/edit", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "Edit inventory") ||
		!strings.Contains(response.Body.String(), `name="user_permissions_`+strconv.FormatUint(uint64(adminUser.ID), 10)+`" value="read" checked`) ||
		!strings.Contains(response.Body.String(), `name="user_permissions_`+strconv.FormatUint(uint64(adminUser.ID), 10)+`" value="update" checked`) ||
		strings.Contains(response.Body.String(), `name="user_permissions_2"`) {
		t.Fatalf("edit inventory response = %d body=%q", response.Code, response.Body.String())
	}
	response = adminRequest(t, handler, http.MethodPost, "/dashboard/inventories/1", url.Values{
		"name":   {"Documents"},
		"status": {"active"},
		"user_permissions_" + strconv.FormatUint(uint64(adminUser.ID), 10): {"read", "delete"},
	}, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard/inventories/1" {
		t.Fatalf("update inventory response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	inventoryUser = model.InventoryUser{}
	if err := database.First(&inventoryUser, "inventory_id = ? AND user_id = ?", 1, adminUser.ID).Error; err != nil {
		t.Fatalf("First(updated inventory user) error = %v", err)
	}
	var deletePermission model.InventoryPermission
	if err := database.First(&deletePermission, "inventory_user_id = ? AND permission = ?", inventoryUser.ID, "delete").Error; err != nil {
		t.Fatalf("First(delete inventory permission) error = %v", err)
	}

	now := time.Now().UTC()
	files := make([]model.InventoryFile, 0, 21)
	for i := 1; i <= 21; i++ {
		files = append(files, model.InventoryFile{
			InventoryID: 1,
			RelativeURI: "file-" + strconv.Itoa(i) + ".txt",
			Status:      model.InventoryFileStatusActive,
			Size:        int64(i * 1024),
			Hash:        "hash-" + strconv.Itoa(i),
			Version:     uint(i),
			Created:     now,
			Modified:    now,
		})
	}
	if err := database.Create(&files).Error; err != nil {
		t.Fatalf("Create(files) error = %v", err)
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/inventories/1", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "Documents") ||
		!strings.Contains(response.Body.String(), "Replicas") ||
		!strings.Contains(response.Body.String(), `data-hide-deleted="replicas"`) ||
		!strings.Contains(response.Body.String(), `data-filter-item="replicas"`) ||
		!strings.Contains(response.Body.String(), `>active</span><span class="status-separator">/</span><span class="pill ok">synchronized</span>`) ||
		!strings.Contains(response.Body.String(), "Inventory files") ||
		!strings.Contains(response.Body.String(), "file-1.txt") ||
		!strings.Contains(response.Body.String(), "20 of 21 files, page 1 of 2") ||
		!strings.Contains(response.Body.String(), "/dashboard/inventories/1?page=2&amp;count=20&amp;sort=id&amp;order=asc") ||
		!strings.Contains(response.Body.String(), "Files per page") ||
		!strings.Contains(response.Body.String(), "Order by") ||
		!strings.Contains(response.Body.String(), `name="sort" data-auto-submit`) ||
		!strings.Contains(response.Body.String(), `name="order" value="desc"`) ||
		strings.Contains(response.Body.String(), ">Apply</button>") {
		t.Fatalf("inventory detail response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/inventories/1?page=2&count=20", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "file-21.txt") ||
		!strings.Contains(response.Body.String(), "1 of 21 files, page 2 of 2") ||
		!strings.Contains(response.Body.String(), "/dashboard/inventories/1?page=1&amp;count=20&amp;sort=id&amp;order=asc") {
		t.Fatalf("inventory files page 2 response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/inventories/1?page=2&count=10", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "10 of 21 files, page 2 of 3") ||
		!strings.Contains(response.Body.String(), "/dashboard/inventories/1?page=1&amp;count=10&amp;sort=id&amp;order=asc") ||
		!strings.Contains(response.Body.String(), "/dashboard/inventories/1?page=3&amp;count=10&amp;sort=id&amp;order=asc") {
		t.Fatalf("inventory files custom page size response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/inventories/1?page=1&count=10&sort=size&order=desc", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "file-21.txt") ||
		!strings.Contains(response.Body.String(), `<option value="size" selected>Size</option>`) ||
		!strings.Contains(response.Body.String(), `name="order" value="asc"`) ||
		!strings.Contains(response.Body.String(), `/dashboard/inventories/1?page=2&amp;count=10&amp;sort=size&amp;order=desc`) {
		t.Fatalf("inventory files sorted response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/inventories/1/replicas/new", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `name="storage_profile"`) ||
		!strings.Contains(response.Body.String(), `data-storage-profile-field>Storage profile`) ||
		!strings.Contains(response.Body.String(), `name="storage_profile" disabled`) ||
		!strings.Contains(response.Body.String(), `name="follow_symlinks"`) ||
		!strings.Contains(response.Body.String(), `<option value="aws"`) ||
		!strings.Contains(response.Body.String(), `<option value="backblaze"`) {
		t.Fatalf("new replica form response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/dashboard/inventories/1/replicas", url.Values{
		"node_id":             {"node-b"},
		"uri":                 {"/backup/documents"},
		"type":                {"storage"},
		"upstream_replica_id": {"1"},
		"storage_profile":     {"aws"},
	}, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard/inventories/1" {
		t.Fatalf("create replica response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	var createdReplica model.Replica
	if err := database.First(&createdReplica, 2).Error; err != nil {
		t.Fatalf("First(created replica) error = %v", err)
	}
	if createdReplica.StorageProfile != "aws" {
		t.Fatalf("createdReplica.StorageProfile = %q, want aws", createdReplica.StorageProfile)
	}
	if createdReplica.FollowSymlinks {
		t.Fatal("createdReplica.FollowSymlinks = true, want false")
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/inventories/1/replicas/2/edit", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `name="storage_profile"`) ||
		!strings.Contains(response.Body.String(), `data-storage-profile-field`) ||
		strings.Contains(response.Body.String(), `name="storage_profile" disabled`) ||
		!strings.Contains(response.Body.String(), `<option value="aws" selected`) {
		t.Fatalf("edit replica form response = %d body=%q", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `name="follow_symlinks" type="checkbox" data-follow-symlinks  disabled`) {
		t.Fatalf("edit replica form does not disable follow_symlinks for storage: body=%q", response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/dashboard/inventories/1/replicas/2", url.Values{
		"type":                {"storage"},
		"status":              {"active"},
		"upstream_replica_id": {""},
		"storage_profile":     {"backblaze"},
	}, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard/inventories/1" {
		t.Fatalf("update replica response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	var updatedReplica model.Replica
	if err := database.First(&updatedReplica, 2).Error; err != nil {
		t.Fatalf("First(replica) error = %v", err)
	}
	if updatedReplica.UpstreamReplicaID != nil {
		t.Fatalf("updatedReplica.UpstreamReplicaID = %v, want nil", updatedReplica.UpstreamReplicaID)
	}
	if updatedReplica.StorageProfile != "backblaze" {
		t.Fatalf("updatedReplica.StorageProfile = %q, want backblaze", updatedReplica.StorageProfile)
	}
	if updatedReplica.FollowSymlinks {
		t.Fatal("updatedReplica.FollowSymlinks = true, want false")
	}

	response = adminRequest(t, handler, http.MethodPost, "/dashboard/inventories/1/replicas/2", url.Values{
		"type":                {"filesystem"},
		"status":              {"active"},
		"upstream_replica_id": {""},
		"storage_profile":     {"backblaze"},
		"follow_symlinks":     {"on"},
	}, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard/inventories/1" {
		t.Fatalf("clear replica storage profile response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if err := database.First(&updatedReplica, 2).Error; err != nil {
		t.Fatalf("First(replica after profile clear) error = %v", err)
	}
	if updatedReplica.StorageProfile != "" {
		t.Fatalf("updatedReplica.StorageProfile = %q, want empty for filesystem replica", updatedReplica.StorageProfile)
	}
	if !updatedReplica.FollowSymlinks {
		t.Fatal("updatedReplica.FollowSymlinks = false, want true for filesystem replica")
	}

	if err := database.Create(&model.Node{ID: "node-disabled", Status: model.NodeStatusDisabled, Secret: "ignored"}).Error; err != nil {
		t.Fatalf("Create(disabled node) error = %v", err)
	}
	if err := database.Create(&model.Replica{
		InventoryID: 1,
		NodeID:      "node-disabled",
		URI:         "/disabled/documents",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}).Error; err != nil {
		t.Fatalf("Create(disabled node replica) error = %v", err)
	}
	if err := database.Create(&model.Replica{
		InventoryID: 1,
		NodeID:      "node-a",
		URI:         "/deleted/documents",
		Status:      model.ReplicaStatusDeleted,
		Type:        model.ReplicaTypeFilesystem,
	}).Error; err != nil {
		t.Fatalf("Create(deleted replica) error = %v", err)
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/inventories/1", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `/deleted/documents`) ||
		!strings.Contains(response.Body.String(), `>deleted</span></span></td>`) ||
		strings.Contains(response.Body.String(), `>deleted</span><span class="status-separator">/</span>`) {
		t.Fatalf("deleted replica status response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/shares", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "Shares") ||
		!strings.Contains(response.Body.String(), `data-list-filter="shares" data-filter-field="nodeId"`) ||
		!strings.Contains(response.Body.String(), `data-list-filter="shares" data-filter-field="inventoryId"`) ||
		!strings.Contains(response.Body.String(), `data-hide-deleted="shares"`) ||
		!strings.Contains(response.Body.String(), `href="/dashboard/shares/new"`) {
		t.Fatalf("shares response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/shares/new", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "New share") ||
		!strings.Contains(response.Body.String(), `name="replica_id"`) ||
		!strings.Contains(response.Body.String(), `data-share-node-select`) ||
		!strings.Contains(response.Body.String(), `data-share-replica-select`) ||
		!strings.Contains(response.Body.String(), `name="anonymous_permissions"`) ||
		!strings.Contains(response.Body.String(), `name="enable_expiration"`) ||
		!strings.Contains(response.Body.String(), `name="property_view"`) ||
		!strings.Contains(response.Body.String(), `name="property_page_size"`) ||
		!strings.Contains(response.Body.String(), `name="property_thumbnail_size"`) ||
		!strings.Contains(response.Body.String(), `name="property_theme"`) ||
		!strings.Contains(response.Body.String(), `placeholder="Unset"`) ||
		!strings.Contains(response.Body.String(), `<option value="256"`) ||
		strings.Contains(response.Body.String(), `>null<`) ||
		!strings.Contains(response.Body.String(), `#1 Documents - Replica #1`) ||
		!strings.Contains(response.Body.String(), `Documents`) ||
		!strings.Contains(response.Body.String(), `value="node-a"`) ||
		strings.Contains(response.Body.String(), `value="node-disabled"`) ||
		strings.Contains(response.Body.String(), `Replica #3`) ||
		strings.Contains(response.Body.String(), `Replica #4`) ||
		strings.Contains(response.Body.String(), `name="user_permissions_2"`) {
		t.Fatalf("new share response = %d body=%q", response.Code, response.Body.String())
	}

	expiresAt := "2026-03-17"
	response = adminRequest(t, handler, http.MethodPost, "/dashboard/shares", url.Values{
		"replica_id": {"1"},
		"name":       {""},
		"user_permissions_" + strconv.FormatUint(uint64(adminUser.ID), 10): {"read", "update", "delete"},
		"anonymous_permissions":   {"read", "update"},
		"enable_expiration":       {"1"},
		"share_expiration":        {expiresAt},
		"property_view":           {"grid"},
		"property_page_size":      {"100"},
		"property_thumbnail_size": {"256"},
		"property_theme":          {"dark"},
	}, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard/shares" {
		t.Fatalf("create share response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	var createdShare model.Share
	if err := database.First(&createdShare, 1).Error; err != nil {
		t.Fatalf("First(created share) error = %v", err)
	}
	if createdShare.LinkHash == nil || *createdShare.LinkHash == "" {
		t.Fatalf("createdShare.LinkHash = %v, want generated value", createdShare.LinkHash)
	}
	parsedExpiresAt, err := time.Parse(time.RFC3339, "2026-03-17T00:00:00Z")
	if err != nil {
		t.Fatalf("Parse(expiresAt) error = %v", err)
	}
	if createdShare.ShareExpiration == nil || !createdShare.ShareExpiration.Equal(parsedExpiresAt) {
		t.Fatalf("createdShare.ShareExpiration = %v, want %v", createdShare.ShareExpiration, parsedExpiresAt)
	}
	if createdShare.Properties.View == nil || *createdShare.Properties.View != "grid" ||
		createdShare.Properties.PageSize == nil || *createdShare.Properties.PageSize != 100 ||
		createdShare.Properties.ThumbnailSize == nil || *createdShare.Properties.ThumbnailSize != 256 ||
		createdShare.Properties.Theme == nil || *createdShare.Properties.Theme != "dark" {
		t.Fatalf("createdShare.Properties = %+v, want configured appearance", createdShare.Properties)
	}
	response = adminRequest(t, handler, http.MethodGet, "/dashboard/inventories", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `folder · Inventory #1 · 3 replicas - 1 shares`) {
		t.Fatalf("inventories share count response = %d body=%q", response.Code, response.Body.String())
	}
	var shareUser model.ShareUser
	if err := database.First(&shareUser, "share_id = ? AND user_id = ?", createdShare.ID, adminUser.ID).Error; err != nil {
		t.Fatalf("First(share user) error = %v", err)
	}
	var userPermissionCount int64
	if err := database.Model(&model.SharePermission{}).Where("share_user_id = ?", shareUser.ID).Count(&userPermissionCount).Error; err != nil {
		t.Fatalf("Count(user share permissions) error = %v", err)
	}
	if userPermissionCount != 3 {
		t.Fatalf("userPermissionCount = %d, want 3", userPermissionCount)
	}
	var anonymousShareUser model.ShareUser
	if err := database.First(&anonymousShareUser, "share_id = ? AND anonymous = ?", createdShare.ID, true).Error; err != nil {
		t.Fatalf("First(anonymous share user) error = %v", err)
	}
	var anonymousPermissionCount int64
	if err := database.Model(&model.SharePermission{}).Where("share_user_id = ?", anonymousShareUser.ID).Count(&anonymousPermissionCount).Error; err != nil {
		t.Fatalf("Count(anonymous share permissions) error = %v", err)
	}
	if anonymousPermissionCount != 2 {
		t.Fatalf("anonymousPermissionCount = %d, want 2", anonymousPermissionCount)
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/shares", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `data-filter-item="shares"`) ||
		!strings.Contains(response.Body.String(), `data-node-id="node-a"`) ||
		!strings.Contains(response.Body.String(), `data-inventory-id="1"`) ||
		!strings.Contains(response.Body.String(), `<option value="node-a">node-a</option>`) ||
		!strings.Contains(response.Body.String(), `<option value="1">#1 · Documents</option>`) ||
		!strings.Contains(response.Body.String(), `Share #1`) ||
		!strings.Contains(response.Body.String(), `Inventory #1`) ||
		!strings.Contains(response.Body.String(), `href="/dashboard/inventories/1">Open inventory</a>`) ||
		!strings.Contains(response.Body.String(), `Documents`) ||
		!strings.Contains(response.Body.String(), `node: node-a`) ||
		!strings.Contains(response.Body.String(), `anonymous access enabled at `+*createdShare.LinkHash) {
		t.Fatalf("created share response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/shares/1/edit", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "Edit share") ||
		!strings.Contains(response.Body.String(), `value="Documents"`) ||
		!strings.Contains(response.Body.String(), `name="status"`) ||
		!strings.Contains(response.Body.String(), `Anonymous access is enabled.`) ||
		!strings.Contains(response.Body.String(), `value="2026-03-17"`) ||
		!strings.Contains(response.Body.String(), `<option value="grid" selected>Grid</option>`) ||
		!strings.Contains(response.Body.String(), `name="property_page_size" type="number" min="1" step="1" value="100"`) ||
		!strings.Contains(response.Body.String(), `<option value="256" selected>256</option>`) ||
		!strings.Contains(response.Body.String(), `<option value="dark" selected>Dark</option>`) ||
		strings.Contains(response.Body.String(), `>null<`) ||
		strings.Contains(response.Body.String(), `name="user_permissions_2"`) {
		t.Fatalf("edit share response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/dashboard/shares/1", url.Values{
		"name":                    {"Documents shared"},
		"status":                  {"active"},
		"property_view":           {"list"},
		"property_page_size":      {""},
		"property_thumbnail_size": {"512"},
		"property_theme":          {""},
	}, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard/shares" {
		t.Fatalf("update share response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	var updatedShare model.Share
	if err := database.First(&updatedShare, 1).Error; err != nil {
		t.Fatalf("First(updated share) error = %v", err)
	}
	if updatedShare.LinkHash != nil {
		t.Fatalf("updatedShare.LinkHash = %v, want nil after disabling anonymous access", *updatedShare.LinkHash)
	}
	if updatedShare.ShareExpiration != nil {
		t.Fatalf("updatedShare.ShareExpiration = %v, want nil after disabling expiration", updatedShare.ShareExpiration)
	}
	if updatedShare.Properties.View == nil || *updatedShare.Properties.View != "list" ||
		updatedShare.Properties.PageSize != nil ||
		updatedShare.Properties.ThumbnailSize == nil || *updatedShare.Properties.ThumbnailSize != 512 ||
		updatedShare.Properties.Theme != nil {
		t.Fatalf("updatedShare.Properties = %+v, want edited appearance with unset page size and theme", updatedShare.Properties)
	}

	response = adminRequest(t, handler, http.MethodPost, "/dashboard/shares/1/delete", nil, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard/shares" {
		t.Fatalf("delete share response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/shares", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "Documents shared") ||
		!strings.Contains(response.Body.String(), `data-filter-item="shares" data-status="deleted"`) {
		t.Fatalf("deleted share response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/dashboard/inventories/999/delete", nil, accessToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "inventory not found") {
		t.Fatalf("delete missing inventory response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/dashboard/inventories/1/delete", nil, accessToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "inventory has active replicas") {
		t.Fatalf("delete active inventory response = %d body=%q", response.Code, response.Body.String())
	}

	for _, replicaID := range []string{"3", "2", "1"} {
		response = adminRequest(t, handler, http.MethodPost, "/dashboard/inventories/1/replicas/"+replicaID+"/delete", nil, accessToken)
		if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard/inventories/1" {
			t.Fatalf("delete replica %s response = %d location=%q body=%q", replicaID, response.Code, response.Header().Get("Location"), response.Body.String())
		}
	}

	response = adminRequest(t, handler, http.MethodPost, "/dashboard/inventories/1/delete", nil, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard/inventories" {
		t.Fatalf("delete inventory response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	var deletedInventory model.Inventory
	if err := database.First(&deletedInventory, 1).Error; err != nil {
		t.Fatalf("First(deleted inventory) error = %v", err)
	}
	if deletedInventory.Status != model.InventoryStatusDeleted {
		t.Fatalf("deletedInventory.Status = %q, want %q", deletedInventory.Status, model.InventoryStatusDeleted)
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/nodes", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "node-a") ||
		!strings.Contains(response.Body.String(), "<th>Sharing</th>") ||
		!strings.Contains(response.Body.String(), `<span class="pill ok">enabled</span>`) {
		t.Fatalf("nodes response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminJSONRequest(t, handler, http.MethodPost, "/api/admin/nodes", map[string]any{
		"id":              "node-api",
		"secret":          "plain-secret",
		"address":         "http://node-api:8081",
		"status":          "offline",
		"sharing_enabled": true,
	}, accessToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"sharing_enabled":true`) {
		t.Fatalf("create API node response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/api/admin/nodes/node-api", nil, accessToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"sharing_enabled":true`) {
		t.Fatalf("get API node response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminJSONRequest(t, handler, http.MethodPatch, "/api/admin/nodes/node-api", map[string]any{
		"sharing_enabled": false,
	}, accessToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"sharing_enabled":false`) {
		t.Fatalf("patch API node response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/nodes", nil, accessToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `<span class="pill neutral">disabled</span>`) {
		t.Fatalf("nodes sharing disabled response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/nodes/new", nil, accessToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `name="sharing_enabled"`) || !strings.Contains(response.Body.String(), "Enable sharing") {
		t.Fatalf("new node response = %d body=%q", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `data-node-config-preview`) ||
		!strings.Contains(response.Body.String(), `data-coordinator-url="http://coordinator.test:8080"`) ||
		!strings.Contains(response.Body.String(), `data-heartbeat-interval="60s"`) ||
		!strings.Contains(response.Body.String(), `data-http-address=":8080"`) ||
		!strings.Contains(response.Body.String(), `data-node-config-output`) {
		t.Fatalf("new node config preview response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/nodes/node-a/edit", nil, accessToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `name="sharing_enabled" type="checkbox" checked`) {
		t.Fatalf("edit sharing node response = %d body=%q", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), `data-node-config-preview`) {
		t.Fatalf("edit node response unexpectedly included config preview body=%q", response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/dashboard/nodes", url.Values{
		"id":              {"node-sharing"},
		"secret":          {"plain-secret"},
		"address":         {"http://node-sharing:8081"},
		"status":          {"offline"},
		"sharing_enabled": {"on"},
	}, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard/nodes" {
		t.Fatalf("create sharing node response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	var sharingNode model.Node
	if err := database.First(&sharingNode, "id = ?", "node-sharing").Error; err != nil {
		t.Fatalf("First(sharing node) error = %v", err)
	}
	if !sharingNode.Sharing {
		t.Fatal("sharingNode.Sharing = false, want true")
	}

	response = adminRequest(t, handler, http.MethodPost, "/dashboard/nodes/node-sharing", url.Values{
		"address": {"http://node-sharing:8082"},
	}, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard/nodes" {
		t.Fatalf("update sharing node response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if err := database.First(&sharingNode, "id = ?", "node-sharing").Error; err != nil {
		t.Fatalf("First(updated sharing node) error = %v", err)
	}
	if sharingNode.Sharing {
		t.Fatal("sharingNode.Sharing = true, want false")
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/settings", nil, accessToken)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "Settings") ||
		!strings.Contains(response.Body.String(), `href="/dashboard/settings" class="active"`) ||
		!strings.Contains(response.Body.String(), `name="sharing.thumbnail_sizes"`) ||
		!strings.Contains(response.Body.String(), `name="sharing.thumbnail_default_size"`) ||
		!strings.Contains(response.Body.String(), `name="sharing.thumbnails_generate_for_video"`) ||
		!strings.Contains(response.Body.String(), `name="sharing.video_inline_max_size_mb"`) ||
		!strings.Contains(response.Body.String(), `name="sharing.video_playback_enabled"`) {
		t.Fatalf("settings response = %d body=%q", response.Code, response.Body.String())
	}

	response = adminRequest(t, handler, http.MethodPost, "/dashboard/settings", url.Values{
		"sharing.thumbnail_sizes":               {"128, 512"},
		"sharing.thumbnail_default_size":        {"512"},
		"sharing.thumbnails_generate_for_video": {"false"},
		"sharing.video_inline_max_size_mb":      {"50"},
		"sharing.video_playback_enabled":        {"true"},
	}, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard/settings" {
		t.Fatalf("update settings response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	var setting model.Setting
	if err := database.First(&setting, "key = ?", "sharing.video_inline_max_size_mb").Error; err != nil {
		t.Fatalf("First(video setting) error = %v", err)
	}
	if setting.Value != "50" {
		t.Fatalf("video setting = %q, want 50", setting.Value)
	}

	response = adminRequest(t, handler, http.MethodPost, "/dashboard/settings/sharing.video_inline_max_size_mb/reset", nil, accessToken)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard/settings" {
		t.Fatalf("reset setting response = %d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	var settingCount int64
	if err := database.Model(&model.Setting{}).Where("key = ?", "sharing.video_inline_max_size_mb").Count(&settingCount).Error; err != nil {
		t.Fatalf("Count(video setting) error = %v", err)
	}
	if settingCount != 0 {
		t.Fatalf("video setting count = %d, want 0", settingCount)
	}

	response = adminRequest(t, handler, http.MethodGet, "/dashboard/nodes", nil, "invalid")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token response = %d body=%q, want 401", response.Code, response.Body.String())
	}
}

func adminRequest(t *testing.T, handler http.Handler, method, path string, form url.Values, accessToken string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if accessToken != "" {
		request.Header.Set("Authorization", "Bearer "+accessToken)
	}
	request.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func adminJSONRequest(t *testing.T, handler http.Handler, method, path string, body any, accessToken string) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	if accessToken != "" {
		request.Header.Set("Authorization", "Bearer "+accessToken)
	}
	request.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

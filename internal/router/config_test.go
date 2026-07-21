package router

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
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
	if err := database.Create(&model.Node{ID: "node-a", Status: model.NodeStatusOnline, Secret: hashedSecret, Sharing: true}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	authService := newRouterTestAuthService(database)
	pair, err := authService.NodeLogin("node-a", "node-secret", "", "", "", "")
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
	if len(items) != 6 {
		t.Fatalf("items = %d, want 6", len(items))
	}
	if items[0].Key != config.SettingSharingThumbnailSizes {
		t.Fatalf("first key = %q, want %q", items[0].Key, config.SettingSharingThumbnailSizes)
	}
	if got := configItemBool(t, items, "sharing.enabled"); !got {
		t.Fatal("sharing.enabled = false, want true")
	}
}

func TestNodeStorageProfilesRejectsUnauthenticatedRequest(t *testing.T) {
	handler := newConfigRouteHandler(openRouterTestDB(t))

	req := httptest.NewRequest(http.MethodGet, "/node/config/storage-profiles", nil)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusUnauthorized)
	}
}

func TestNodeStorageProfilesRejectsDisabledAndRevokedNodes(t *testing.T) {
	for _, status := range []model.NodeStatus{model.NodeStatusDisabled, model.NodeStatusRevoked} {
		t.Run(string(status), func(t *testing.T) {
			database := openRouterTestDB(t)
			publicKey, _, err := service.GenerateTransferKeyPairPEM()
			if err != nil {
				t.Fatalf("GenerateTransferKeyPairPEM() error = %v", err)
			}
			if err := database.Create(&model.Node{ID: "node-a", Status: status, Secret: "ignored", PublicKey: publicKey}).Error; err != nil {
				t.Fatalf("Create(node) error = %v", err)
			}
			token, err := security.GenerateNodeAccessToken([]byte("test-secret"), "node-a", time.Now().UTC().Add(30*time.Minute))
			if err != nil {
				t.Fatalf("GenerateNodeAccessToken() error = %v", err)
			}
			handler := newConfigRouteHandler(database)

			req := httptest.NewRequest(http.MethodGet, "/node/config/storage-profiles", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("X-API-Version", "1")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusForbidden)
			}
		})
	}
}

func TestNodeStorageProfilesReturnsEmptyWhenNoAssignedReplicaReferencesProfiles(t *testing.T) {
	database := openRouterTestDB(t)
	token := createConfigNodeToken(t, database, "node-a", "node-secret", true)
	handler := newConfigRouteHandler(database)

	req := httptest.NewRequest(http.MethodGet, "/node/config/storage-profiles", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusOK)
	}
	var profiles []service.StorageProfileDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &profiles); err != nil {
		t.Fatalf("Unmarshal(response) error = %v", err)
	}
	if len(profiles) != 0 {
		t.Fatalf("profiles = %+v, want empty", profiles)
	}
}

func TestNodeStorageProfilesReturnsReferencedProfilesEncrypted(t *testing.T) {
	database := openRouterTestDB(t)
	token, privateKey := createConfigNodeTokenAndPrivateKey(t, database, "node-a", "node-secret", true)
	if err := database.Create(&model.Node{ID: "node-b", Status: model.NodeStatusOnline, Secret: "ignored"}).Error; err != nil {
		t.Fatalf("Create(node-b) error = %v", err)
	}
	inventory := model.Inventory{Name: "documents", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(&inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	replicas := []model.Replica{
		{InventoryID: inventory.ID, NodeID: "node-a", URI: "/data/a", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeStorage, StorageProfile: "aws"},
		{InventoryID: inventory.ID, NodeID: "node-a", URI: "/data/b", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeStorage, StorageProfile: "aws"},
		{InventoryID: inventory.ID, NodeID: "node-a", URI: "/data/c", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeStorage, StorageProfile: "backblaze"},
		{InventoryID: inventory.ID, NodeID: "node-a", URI: "/data/d", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem},
		{InventoryID: inventory.ID, NodeID: "node-b", URI: "/data/e", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeStorage, StorageProfile: "unused"},
	}
	if err := database.Create(&replicas).Error; err != nil {
		t.Fatalf("Create(replicas) error = %v", err)
	}
	handler := newConfigRouteHandler(database)

	req := httptest.NewRequest(http.MethodGet, "/node/config/storage-profiles", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusOK)
	}
	body := recorder.Body.String()
	for _, secret := range []string{"aws-access", "aws-secret", "backblaze-access", "backblaze-secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response contains plaintext credential %q: %s", secret, body)
		}
	}
	if strings.Contains(body, "unused") {
		t.Fatalf("response contains unused profile: %s", body)
	}

	var profiles []service.StorageProfileDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &profiles); err != nil {
		t.Fatalf("Unmarshal(response) error = %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("len(profiles) = %d, want 2: %+v", len(profiles), profiles)
	}
	var rawProfiles []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &rawProfiles); err != nil {
		t.Fatalf("Unmarshal(raw response) error = %v", err)
	}
	seen := map[string]service.StorageProfileDetails{}
	for i, profile := range profiles {
		if _, ok := seen[profile.Name]; ok {
			t.Fatalf("duplicate profile %q in response", profile.Name)
		}
		if profile.EncryptedKey == "" || profile.Nonce == "" || profile.Payload == "" {
			t.Fatalf("profile %q has empty encrypted fields: %+v", profile.Name, profile)
		}
		wantFields := map[string]struct{}{
			"name":          {},
			"encrypted_key": {},
			"nonce":         {},
			"payload":       {},
		}
		for field := range rawProfiles[i] {
			if _, ok := wantFields[field]; !ok {
				t.Fatalf("profile %q contains undocumented field %q in %s", profile.Name, field, body)
			}
		}
		seen[profile.Name] = profile
	}

	awsPlaintext := decryptStorageProfilePayloadForTest(t, privateKey, seen["aws"])
	if awsPlaintext["region"] != "eu-central-1" {
		t.Fatalf("aws encrypted region = %q, want eu-central-1", awsPlaintext["region"])
	}
	if awsPlaintext["access_key_id"] != "aws-access" || awsPlaintext["secret_access_key"] != "aws-secret" {
		t.Fatalf("aws encrypted credentials = %+v, want configured credentials", awsPlaintext)
	}
	backblazePlaintext := decryptStorageProfilePayloadForTest(t, privateKey, seen["backblaze"])
	if backblazePlaintext["endpoint"] != "https://s3.eu-central-003.backblazeb2.com" {
		t.Fatalf("backblaze encrypted endpoint = %q, want configured endpoint", backblazePlaintext["endpoint"])
	}
}

func TestNodeStorageProfilesRejectsMissingPublicKey(t *testing.T) {
	database := openRouterTestDB(t)
	token := createConfigNodeToken(t, database, "node-a", "node-secret", false)
	inventory := model.Inventory{Name: "documents", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(&inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	if err := database.Create(&model.Replica{
		InventoryID: inventory.ID, NodeID: "node-a", URI: "/data/a", Status: model.ReplicaStatusActive,
		Type: model.ReplicaTypeStorage, StorageProfile: "aws",
	}).Error; err != nil {
		t.Fatalf("Create(replica) error = %v", err)
	}
	handler := newConfigRouteHandler(database)

	req := httptest.NewRequest(http.MethodGet, "/node/config/storage-profiles", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-API-Version", "1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusConflict)
	}
}

func newConfigRouteHandler(database *gorm.DB) http.Handler {
	return newConfigRouteHandlerWithAuth(database, newRouterTestAuthService(database))
}

func newConfigRouteHandlerWithAuth(database *gorm.DB, authService *service.AuthService) http.Handler {
	return newConfigRouteHandlerWithConfigAndAuth(database, authService, config.Config{
		Storage: config.StorageConfig{Profiles: map[string]config.StorageProfileConfig{
			"aws": {
				AccessKeyID:     "aws-access",
				SecretAccessKey: "aws-secret",
				Region:          "eu-central-1",
			},
			"backblaze": {
				AccessKeyID:     "backblaze-access",
				SecretAccessKey: "backblaze-secret",
				Region:          "eu-central-003",
				Endpoint:        "https://s3.eu-central-003.backblazeb2.com",
			},
			"unused": {
				AccessKeyID:     "unused-access",
				SecretAccessKey: "unused-secret",
			},
		}},
	})
}

func newConfigRouteHandlerWithConfigAndAuth(database *gorm.DB, authService *service.AuthService, cfg config.Config) http.Handler {
	cfg.Sharing = config.SharingConfig{
		ThumbnailSizes:             []int{128, 256, 512},
		ThumbnailDefaultSize:       256,
		ThumbnailsGenerateForVideo: true,
		VideoInlineMaxSizeMB:       25,
		VideoPlaybackEnabled:       true,
	}
	configService := service.NewConfigService(repository.NewConfigRepository(database), config.Config{
		Sharing: cfg.Sharing,
	})
	return New(
		cfg,
		buildinfo.Info{Version: "test"},
		authService,
		service.NewUserService(repository.NewUserRepository(database), repository.NewRoleRepository(database)),
		service.NewRoleService(repository.NewRoleRepository(database)),
		service.NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database)),
		service.NewInventoryService(repository.NewInventoryRepository(database)),
		service.NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database)),
		nil,
		nil,
		configService,
	)
}

func createConfigNodeToken(t *testing.T, database *gorm.DB, nodeID string, secret string, includePublicKey bool) string {
	t.Helper()
	token, _ := createConfigNodeTokenAndPrivateKey(t, database, nodeID, secret, includePublicKey)
	return token
}

func createConfigNodeTokenAndPrivateKey(t *testing.T, database *gorm.DB, nodeID string, secret string, includePublicKey bool) (string, string) {
	t.Helper()

	hashedSecret, err := security.HashPassword(secret)
	if err != nil {
		t.Fatalf("HashPassword(node) error = %v", err)
	}
	if err := database.Create(&model.Node{ID: nodeID, Status: model.NodeStatusOnline, Secret: hashedSecret}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	publicKey := ""
	privateKey := ""
	if includePublicKey {
		publicKey, privateKey, err = service.GenerateTransferKeyPairPEM()
		if err != nil {
			t.Fatalf("GenerateTransferKeyPairPEM() error = %v", err)
		}
	}
	authService := newRouterTestAuthService(database)
	pair, err := authService.NodeLogin(nodeID, secret, publicKey, "", "", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}
	return pair.AccessToken, privateKey
}

func decryptStorageProfilePayloadForTest(t *testing.T, privateKeyPEM string, profile service.StorageProfileDetails) map[string]string {
	t.Helper()

	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		t.Fatal("private key PEM missing block")
	}
	privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("ParsePKCS1PrivateKey() error = %v", err)
	}
	encryptedKey, err := base64.StdEncoding.DecodeString(profile.EncryptedKey)
	if err != nil {
		t.Fatalf("DecodeString(encrypted_key) error = %v", err)
	}
	key, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, privateKey, encryptedKey, nil)
	if err != nil {
		t.Fatalf("DecryptOAEP() error = %v", err)
	}
	blockCipher, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher() error = %v", err)
	}
	gcm, err := cipher.NewGCM(blockCipher)
	if err != nil {
		t.Fatalf("NewGCM() error = %v", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(profile.Nonce)
	if err != nil {
		t.Fatalf("DecodeString(nonce) error = %v", err)
	}
	payload, err := base64.StdEncoding.DecodeString(profile.Payload)
	if err != nil {
		t.Fatalf("DecodeString(payload) error = %v", err)
	}
	plaintext, err := gcm.Open(nil, nonce, payload, nil)
	if err != nil {
		t.Fatalf("Open(payload) error = %v", err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(plaintext, &decoded); err != nil {
		t.Fatalf("Unmarshal(plaintext) error = %v", err)
	}
	return decoded
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

func configItemBool(t *testing.T, items []service.ConfigItem, key string) bool {
	t.Helper()
	for _, item := range items {
		if item.Key == key {
			value, ok := item.Value.(bool)
			if !ok {
				t.Fatalf("item %s has type %T", key, item.Value)
			}
			return value
		}
	}
	t.Fatalf("missing item %s", key)
	return false
}

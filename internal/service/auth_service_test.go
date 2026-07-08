package service

import (
	"path/filepath"
	"testing"
	"time"

	"replica/internal/config"
	"replica/internal/db"
	"replica/internal/model"
	"replica/internal/repository"
	"replica/internal/security"

	"gorm.io/gorm"
)

func newTestAuthService(database *gorm.DB) *AuthService {
	return NewAuthService(
		repository.NewUserRepository(database),
		repository.NewUserTokenRepository(database),
		repository.NewNodeRepository(database),
		repository.NewNodeTokenRepository(database),
		"test-secret",
		30*time.Minute,
		8*time.Hour,
	)
}

func TestAuthServiceLoginStoresRefreshHashAndReturnsJWT(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "auth-login.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
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

	authService := newTestAuthService(database)

	pair, err := authService.Login("jsmith", "secret")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	if pair.AccessToken == "" {
		t.Fatal("AccessToken is empty")
	}
	if pair.RefreshToken == "" {
		t.Fatal("RefreshToken is empty")
	}

	claims, err := security.ParseUserAccessToken([]byte("test-secret"), pair.AccessToken)
	if err != nil {
		t.Fatalf("ParseUserAccessToken() error = %v", err)
	}
	if claims.Subject != "1" {
		t.Fatalf("claims.Subject = %q, want %q", claims.Subject, "1")
	}

	var stored model.UserToken
	if err := database.First(&stored).Error; err != nil {
		t.Fatalf("First(user_token) error = %v", err)
	}
	if stored.RefreshHash == "" {
		t.Fatal("stored.RefreshHash is empty")
	}
	if stored.RefreshHash == pair.RefreshToken {
		t.Fatal("stored.RefreshHash should not equal raw refresh token")
	}
}

func TestAuthServiceRefreshRotatesRefreshToken(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "auth-refresh.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
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

	authService := newTestAuthService(database)

	firstPair, err := authService.Login("jsmith", "secret")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	secondPair, err := authService.Refresh(firstPair.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	if secondPair.RefreshToken == firstPair.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}
	if secondPair.AccessToken == firstPair.AccessToken {
		t.Fatal("access token was not rotated")
	}

	claims, err := security.ParseUserAccessToken([]byte("test-secret"), secondPair.AccessToken)
	if err != nil {
		t.Fatalf("ParseUserAccessToken() error = %v", err)
	}
	if claims.Subject != "1" {
		t.Fatalf("claims.Subject = %q, want %q", claims.Subject, "1")
	}

	if _, err := authService.Refresh(firstPair.RefreshToken); err != ErrInvalidToken {
		t.Fatalf("Refresh(old token) error = %v, want %v", err, ErrInvalidToken)
	}

	var tokens []model.UserToken
	if err := database.Order("id asc").Find(&tokens).Error; err != nil {
		t.Fatalf("Find(user_tokens) error = %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("len(tokens) = %d, want 1", len(tokens))
	}
	if tokens[0].RefreshHash != security.HashOpaqueToken(secondPair.RefreshToken) {
		t.Fatal("stored refresh hash does not match latest refresh token")
	}
}

func TestAuthServiceLogoutRevokesCurrentSession(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "auth-logout.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
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

	authService := newTestAuthService(database)

	pair, err := authService.Login("jsmith", "secret")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	if err := authService.Logout(pair.AccessToken); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}

	if _, err := authService.Refresh(pair.RefreshToken); err != ErrInvalidToken {
		t.Fatalf("Refresh(revoked token) error = %v, want %v", err, ErrInvalidToken)
	}
}

func TestAuthServiceRefreshRejectsDeletedUser(t *testing.T) {
	database := newAuthTestDB(t, "auth-refresh-deleted-user.db")

	hashedPassword, err := security.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	user := &model.User{
		Name:     "jsmith",
		Status:   model.UserStatusDeleted,
		Password: hashedPassword,
	}
	if err := database.Create(user).Error; err != nil {
		t.Fatalf("Create(user) error = %v", err)
	}

	refreshToken := "deleted-user-refresh-token"
	if err := database.Create(&model.UserToken{
		UserID:            user.ID,
		RefreshHash:       security.HashOpaqueToken(refreshToken),
		RefreshExpiration: time.Now().UTC().Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("Create(user_token) error = %v", err)
	}

	authService := newTestAuthService(database)
	if _, err := authService.Refresh(refreshToken); err != ErrInactiveUser {
		t.Fatalf("Refresh(deleted user) error = %v, want %v", err, ErrInactiveUser)
	}
}

func TestUserPasswordChangeInvalidatesRefreshTokens(t *testing.T) {
	database := newAuthTestDB(t, "auth-user-password-change.db")
	user := createAuthTestUser(t, database, "jsmith", model.UserStatusActive, "secret")
	authService := newTestAuthService(database)
	pair, err := authService.Login("jsmith", "secret")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	userService := NewUserService(
		repository.NewUserRepository(database),
		repository.NewRoleRepository(database),
		repository.NewUserTokenRepository(database),
	)
	newPassword := "new-secret"
	if _, err := userService.Update(user.ID, UpdateUserInput{Password: &newPassword}); err != nil {
		t.Fatalf("Update(password) error = %v", err)
	}

	if _, err := authService.Refresh(pair.RefreshToken); err != ErrInvalidToken {
		t.Fatalf("Refresh(old token) error = %v, want %v", err, ErrInvalidToken)
	}
	assertUserTokenCount(t, database, user.ID, 0)
}

func TestUserStatusChangeInvalidatesRefreshTokens(t *testing.T) {
	database := newAuthTestDB(t, "auth-user-status-change.db")
	user := createAuthTestUser(t, database, "jsmith", model.UserStatusActive, "secret")
	authService := newTestAuthService(database)
	pair, err := authService.Login("jsmith", "secret")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	userService := NewUserService(
		repository.NewUserRepository(database),
		repository.NewRoleRepository(database),
		repository.NewUserTokenRepository(database),
	)
	deleted := string(model.UserStatusDeleted)
	if _, err := userService.Update(user.ID, UpdateUserInput{Status: &deleted}); err != nil {
		t.Fatalf("Update(status) error = %v", err)
	}

	if _, err := authService.Refresh(pair.RefreshToken); err != ErrInvalidToken {
		t.Fatalf("Refresh(old token) error = %v, want %v", err, ErrInvalidToken)
	}
	assertUserTokenCount(t, database, user.ID, 0)
}

func TestAuthServiceMeRejectsNodeJWT(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "auth-me-node-jwt.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

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

	authService := newTestAuthService(database)
	pair, err := authService.NodeLogin("node-a", "node-secret", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}

	if _, err := authService.Me(pair.AccessToken); err != ErrInvalidToken {
		t.Fatalf("Me(node jwt) error = %v, want %v", err, ErrInvalidToken)
	}
}

func TestAuthServiceNodeLoginReplacesExistingSession(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "auth-node-login.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	node := &model.Node{
		ID:      "node-a",
		Status:  model.NodeStatusOffline,
		Secret:  hashedSecret,
		Address: "http://old-address:8080",
	}
	if err := database.Create(node).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	if err := database.Create(&model.NodeToken{
		NodeID:            node.ID,
		RefreshHash:       security.HashOpaqueToken("old-refresh"),
		RefreshExpiration: time.Now().UTC().Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("Create(node_token) error = %v", err)
	}

	authService := newTestAuthService(database)

	pair, err := authService.NodeLogin("node-a", "node-secret", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}

	if pair.NodeID != "node-a" {
		t.Fatalf("pair.NodeID = %q, want %q", pair.NodeID, "node-a")
	}
	if pair.AccessToken == "" {
		t.Fatal("AccessToken is empty")
	}
	if pair.RefreshToken == "" {
		t.Fatal("RefreshToken is empty")
	}

	claims, err := security.ParseNodeAccessToken([]byte("test-secret"), pair.AccessToken)
	if err != nil {
		t.Fatalf("ParseNodeAccessToken() error = %v", err)
	}
	if claims.Subject != "node-a" {
		t.Fatalf("claims.Subject = %q, want %q", claims.Subject, "node-a")
	}

	var tokens []model.NodeToken
	if err := database.Order("node_id asc").Find(&tokens).Error; err != nil {
		t.Fatalf("Find(node_tokens) error = %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("len(tokens) = %d, want 1", len(tokens))
	}
	if tokens[0].NodeID != "node-a" {
		t.Fatalf("tokens[0].NodeID = %q, want %q", tokens[0].NodeID, "node-a")
	}
	if tokens[0].RefreshHash != security.HashOpaqueToken(pair.RefreshToken) {
		t.Fatal("stored refresh hash does not match latest node refresh token")
	}

	var storedNode model.Node
	if err := database.First(&storedNode, "id = ?", "node-a").Error; err != nil {
		t.Fatalf("First(node) error = %v", err)
	}
	if storedNode.Address != "http://old-address:8080" {
		t.Fatalf("storedNode.Address = %q, want %q", storedNode.Address, "http://old-address:8080")
	}
	if storedNode.Status != model.NodeStatusOffline {
		t.Fatalf("storedNode.Status = %q, want %q", storedNode.Status, model.NodeStatusOffline)
	}
}

func TestAuthServiceNodeRefreshRotatesRefreshToken(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "auth-node-refresh.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	node := &model.Node{
		ID:      "node-a",
		Status:  model.NodeStatusOffline,
		Secret:  hashedSecret,
		Address: "http://old-address:8080",
	}
	if err := database.Create(node).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}

	authService := newTestAuthService(database)

	firstPair, err := authService.NodeLogin("node-a", "node-secret", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}

	secondPair, err := authService.NodeRefresh(firstPair.RefreshToken)
	if err != nil {
		t.Fatalf("NodeRefresh() error = %v", err)
	}

	if secondPair.RefreshToken == firstPair.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}
	if secondPair.AccessToken == "" {
		t.Fatal("access token is empty")
	}

	claims, err := security.ParseNodeAccessToken([]byte("test-secret"), secondPair.AccessToken)
	if err != nil {
		t.Fatalf("ParseNodeAccessToken() error = %v", err)
	}
	if claims.Subject != "node-a" {
		t.Fatalf("claims.Subject = %q, want %q", claims.Subject, "node-a")
	}

	if _, err := authService.NodeRefresh(firstPair.RefreshToken); err != ErrInvalidToken {
		t.Fatalf("NodeRefresh(old token) error = %v, want %v", err, ErrInvalidToken)
	}

	var stored model.NodeToken
	if err := database.First(&stored, "node_id = ?", "node-a").Error; err != nil {
		t.Fatalf("First(node_token) error = %v", err)
	}
	if stored.RefreshHash != security.HashOpaqueToken(secondPair.RefreshToken) {
		t.Fatal("stored refresh hash does not match rotated node refresh token")
	}

	var storedNode model.Node
	if err := database.First(&storedNode, "id = ?", "node-a").Error; err != nil {
		t.Fatalf("First(node) error = %v", err)
	}
	if storedNode.Address != "http://old-address:8080" {
		t.Fatalf("storedNode.Address = %q, want %q", storedNode.Address, "http://old-address:8080")
	}
	if storedNode.Status != model.NodeStatusOffline {
		t.Fatalf("storedNode.Status = %q, want %q", storedNode.Status, model.NodeStatusOffline)
	}
}

func TestAuthServiceNodeLoginRejectsDisabledNode(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "auth-node-disabled.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	node := &model.Node{
		ID:     "node-a",
		Status: model.NodeStatusDisabled,
		Secret: hashedSecret,
	}
	if err := database.Create(node).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}

	authService := newTestAuthService(database)

	if _, err := authService.NodeLogin("node-a", "node-secret", ""); err != ErrDisabledNode {
		t.Fatalf("NodeLogin(disabled node) error = %v, want %v", err, ErrDisabledNode)
	}
}

func TestAuthServiceNodeLoginRejectsRevokedNode(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "auth-node-revoked.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	node := &model.Node{
		ID:     "node-a",
		Status: model.NodeStatusRevoked,
		Secret: hashedSecret,
	}
	if err := database.Create(node).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}

	authService := newTestAuthService(database)

	if _, err := authService.NodeLogin("node-a", "node-secret", ""); err != ErrRevokedNode {
		t.Fatalf("NodeLogin(revoked node) error = %v, want %v", err, ErrRevokedNode)
	}
}

func TestAuthServiceNodeRefreshRejectsDisabledNode(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "auth-node-refresh-disabled.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	node := &model.Node{
		ID:     "node-a",
		Status: model.NodeStatusDisabled,
		Secret: hashedSecret,
	}
	if err := database.Create(node).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}

	refreshToken := "disabled-refresh-token"
	if err := database.Create(&model.NodeToken{
		NodeID:            node.ID,
		RefreshHash:       security.HashOpaqueToken(refreshToken),
		RefreshExpiration: time.Now().UTC().Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("Create(node_token) error = %v", err)
	}

	authService := newTestAuthService(database)

	if _, err := authService.NodeRefresh(refreshToken); err != ErrDisabledNode {
		t.Fatalf("NodeRefresh(disabled node) error = %v, want %v", err, ErrDisabledNode)
	}
}

func TestAuthServiceNodeRefreshRejectsRevokedNode(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "auth-node-refresh-revoked.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	hashedSecret, err := security.HashPassword("node-secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	node := &model.Node{
		ID:     "node-a",
		Status: model.NodeStatusRevoked,
		Secret: hashedSecret,
	}
	if err := database.Create(node).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}

	refreshToken := "revoked-node-refresh-token"
	if err := database.Create(&model.NodeToken{
		NodeID:            node.ID,
		RefreshHash:       security.HashOpaqueToken(refreshToken),
		RefreshExpiration: time.Now().UTC().Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("Create(node_token) error = %v", err)
	}

	authService := newTestAuthService(database)

	if _, err := authService.NodeRefresh(refreshToken); err != ErrRevokedNode {
		t.Fatalf("NodeRefresh(revoked node) error = %v, want %v", err, ErrRevokedNode)
	}
}

func TestAuthServiceNodeRefreshRejectsRevokedToken(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "auth-node-revoked-token.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

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

	refreshToken := "revoked-refresh-token"
	revokedAt := time.Now().UTC()
	if err := database.Create(&model.NodeToken{
		NodeID:            node.ID,
		RefreshHash:       security.HashOpaqueToken(refreshToken),
		RefreshExpiration: time.Now().UTC().Add(time.Hour),
		RevokedAt:         &revokedAt,
	}).Error; err != nil {
		t.Fatalf("Create(node_token) error = %v", err)
	}

	authService := newTestAuthService(database)

	if _, err := authService.NodeRefresh(refreshToken); err != ErrRevokedToken {
		t.Fatalf("NodeRefresh(revoked token) error = %v, want %v", err, ErrRevokedToken)
	}
}

func TestNodeSecretChangeInvalidatesRefreshToken(t *testing.T) {
	database := newAuthTestDB(t, "auth-node-secret-change.db")
	createAuthTestNode(t, database, "node-a", model.NodeStatusOffline, "node-secret")
	authService := newTestAuthService(database)
	pair, err := authService.NodeLogin("node-a", "node-secret", "")
	if err != nil {
		t.Fatalf("NodeLogin() error = %v", err)
	}

	nodeService := NewNodeService(
		repository.NewNodeRepository(database),
		repository.NewNodeCommandRepository(database),
		repository.NewNodeTokenRepository(database),
	)
	newSecret := "new-node-secret"
	if _, err := nodeService.Update("node-a", UpdateNodeInput{Secret: &newSecret}); err != nil {
		t.Fatalf("Update(secret) error = %v", err)
	}

	if _, err := authService.NodeRefresh(pair.RefreshToken); err != ErrInvalidToken {
		t.Fatalf("NodeRefresh(old token) error = %v, want %v", err, ErrInvalidToken)
	}
	assertNodeTokenCount(t, database, "node-a", 0)
}

func TestNodeStatusChangeInvalidatesRefreshToken(t *testing.T) {
	for _, status := range []model.NodeStatus{model.NodeStatusDisabled, model.NodeStatusRevoked} {
		t.Run(string(status), func(t *testing.T) {
			database := newAuthTestDB(t, "auth-node-status-change-"+string(status)+".db")
			createAuthTestNode(t, database, "node-a", model.NodeStatusOffline, "node-secret")
			authService := newTestAuthService(database)
			pair, err := authService.NodeLogin("node-a", "node-secret", "")
			if err != nil {
				t.Fatalf("NodeLogin() error = %v", err)
			}

			nodeService := NewNodeService(
				repository.NewNodeRepository(database),
				repository.NewNodeCommandRepository(database),
				repository.NewNodeTokenRepository(database),
			)
			statusValue := string(status)
			if _, err := nodeService.Update("node-a", UpdateNodeInput{Status: &statusValue}); err != nil {
				t.Fatalf("Update(status) error = %v", err)
			}

			if _, err := authService.NodeRefresh(pair.RefreshToken); err != ErrInvalidToken {
				t.Fatalf("NodeRefresh(old token) error = %v, want %v", err, ErrInvalidToken)
			}
			assertNodeTokenCount(t, database, "node-a", 0)
		})
	}
}

func newAuthTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()

	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), name),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}
	return database
}

func createAuthTestUser(t *testing.T, database *gorm.DB, name string, status model.UserStatus, password string) *model.User {
	t.Helper()

	hashedPassword, err := security.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	user := &model.User{
		Name:     name,
		Status:   status,
		Password: hashedPassword,
	}
	if err := database.Create(user).Error; err != nil {
		t.Fatalf("Create(user) error = %v", err)
	}
	return user
}

func createAuthTestNode(t *testing.T, database *gorm.DB, id string, status model.NodeStatus, secret string) *model.Node {
	t.Helper()

	hashedSecret, err := security.HashPassword(secret)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	node := &model.Node{
		ID:     id,
		Status: status,
		Secret: hashedSecret,
	}
	if err := database.Create(node).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	return node
}

func assertUserTokenCount(t *testing.T, database *gorm.DB, userID uint, want int64) {
	t.Helper()

	var count int64
	if err := database.Model(&model.UserToken{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		t.Fatalf("Count(user_tokens) error = %v", err)
	}
	if count != want {
		t.Fatalf("user token count = %d, want %d", count, want)
	}
}

func assertNodeTokenCount(t *testing.T, database *gorm.DB, nodeID string, want int64) {
	t.Helper()

	var count int64
	if err := database.Model(&model.NodeToken{}).Where("node_id = ?", nodeID).Count(&count).Error; err != nil {
		t.Fatalf("Count(node_tokens) error = %v", err)
	}
	if count != want {
		t.Fatalf("node token count = %d, want %d", count, want)
	}
}

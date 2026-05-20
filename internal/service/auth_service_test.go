package service

import (
	"path/filepath"
	"testing"
	"time"

	"dropoutbox/internal/config"
	"dropoutbox/internal/db"
	"dropoutbox/internal/model"
	"dropoutbox/internal/repository"
	"dropoutbox/internal/security"
)

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

	authService := NewAuthService(
		repository.NewUserRepository(database),
		repository.NewUserTokenRepository(database),
		"test-secret",
		30*time.Minute,
		8*time.Hour,
	)

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

	authService := NewAuthService(
		repository.NewUserRepository(database),
		repository.NewUserTokenRepository(database),
		"test-secret",
		30*time.Minute,
		8*time.Hour,
	)

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

	authService := NewAuthService(
		repository.NewUserRepository(database),
		repository.NewUserTokenRepository(database),
		"test-secret",
		30*time.Minute,
		8*time.Hour,
	)

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

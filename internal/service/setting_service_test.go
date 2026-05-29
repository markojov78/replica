package service

import (
	"errors"
	"path/filepath"
	"testing"

	"dropoutbox/internal/config"
	"dropoutbox/internal/db"
	"dropoutbox/internal/model"
	"dropoutbox/internal/repository"

	"gorm.io/gorm"
)

func TestEnsureTransferKeysCreatesKeyPair(t *testing.T) {
	database := newSettingTestDB(t)
	service := NewSettingService(repository.NewSettingRepository(database))

	if err := service.EnsureTransferKeys(); err != nil {
		t.Fatalf("EnsureTransferKeys() error = %v", err)
	}

	publicKey, err := service.TransferPublicKey()
	if err != nil {
		t.Fatalf("TransferPublicKey() error = %v", err)
	}
	if publicKey == "" {
		t.Fatal("TransferPublicKey() = empty")
	}

	var private model.Setting
	if err := database.First(&private, "key = ?", SettingTransferKeyPrivate).Error; err != nil {
		t.Fatalf("private key setting missing: %v", err)
	}
	if private.Value == "" {
		t.Fatal("private key setting is empty")
	}
}

func TestEnsureTransferKeysDoesNotOverwriteExistingPair(t *testing.T) {
	database := newSettingTestDB(t)
	if err := database.Create(&model.Setting{Key: SettingTransferKeyPublic, Value: "public"}).Error; err != nil {
		t.Fatalf("create public setting: %v", err)
	}
	if err := database.Create(&model.Setting{Key: SettingTransferKeyPrivate, Value: "private"}).Error; err != nil {
		t.Fatalf("create private setting: %v", err)
	}

	service := NewSettingService(repository.NewSettingRepository(database))
	if err := service.EnsureTransferKeys(); err != nil {
		t.Fatalf("EnsureTransferKeys() error = %v", err)
	}

	publicKey, err := service.TransferPublicKey()
	if err != nil {
		t.Fatalf("TransferPublicKey() error = %v", err)
	}
	if publicKey != "public" {
		t.Fatalf("TransferPublicKey() = %q, want existing public key", publicKey)
	}
}

func TestEnsureTransferKeysRejectsIncompletePair(t *testing.T) {
	database := newSettingTestDB(t)
	if err := database.Create(&model.Setting{Key: SettingTransferKeyPublic, Value: "public"}).Error; err != nil {
		t.Fatalf("create public setting: %v", err)
	}

	service := NewSettingService(repository.NewSettingRepository(database))
	err := service.EnsureTransferKeys()
	if !errors.Is(err, ErrIncompleteTransferKeys) {
		t.Fatalf("EnsureTransferKeys() error = %v, want %v", err, ErrIncompleteTransferKeys)
	}
}

func newSettingTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "settings.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}
	return database
}

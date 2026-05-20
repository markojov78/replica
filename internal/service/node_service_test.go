package service

import (
	"path/filepath"
	"testing"

	"dropoutbox/internal/config"
	"dropoutbox/internal/db"
	"dropoutbox/internal/model"
	"dropoutbox/internal/repository"
	"dropoutbox/internal/security"
)

func TestNodeServiceCreateHashesSecretAndDefaultsOffline(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "node-create.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	nodeService := NewNodeService(repository.NewNodeRepository(database))

	node, err := nodeService.Create("node-a", "plain-secret", "http://node-a:8081", nil)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if node.ID != "node-a" {
		t.Fatalf("node.ID = %q, want %q", node.ID, "node-a")
	}
	if node.Status != string(model.NodeStatusOffline) {
		t.Fatalf("node.Status = %q, want %q", node.Status, model.NodeStatusOffline)
	}

	var stored model.Node
	if err := database.First(&stored, "id = ?", "node-a").Error; err != nil {
		t.Fatalf("First(node) error = %v", err)
	}
	if stored.Secret == "plain-secret" {
		t.Fatal("stored secret should be hashed")
	}
	if err := security.CheckPassword(stored.Secret, "plain-secret"); err != nil {
		t.Fatalf("CheckPassword() error = %v", err)
	}
}

func TestNodeServiceDeleteRevokesNode(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "node-delete.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	hashedSecret, err := security.HashPassword("plain-secret")
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

	nodeService := NewNodeService(repository.NewNodeRepository(database))

	node, err := nodeService.Delete("node-a")
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if node.Status != string(model.NodeStatusRevoked) {
		t.Fatalf("node.Status = %q, want %q", node.Status, model.NodeStatusRevoked)
	}
}

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

	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))

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

	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))

	node, err := nodeService.Delete("node-a")
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if node.Status != string(model.NodeStatusRevoked) {
		t.Fatalf("node.Status = %q, want %q", node.Status, model.NodeStatusRevoked)
	}
}

func TestNodeServiceReportAvailabilityUpdatesAddressAndLastSeen(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "node-report.db"),
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
		ID:      "node-a",
		Status:  model.NodeStatusOffline,
		Secret:  hashedSecret,
		Address: "http://old-address:8081",
	}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}

	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))

	report, err := nodeService.ReportAvailability("node-a", "https://node-address:8081")
	if err != nil {
		t.Fatalf("ReportAvailability() error = %v", err)
	}
	if report.NodeID != "node-a" {
		t.Fatalf("report.NodeID = %q, want %q", report.NodeID, "node-a")
	}
	if report.Address != "https://node-address:8081" {
		t.Fatalf("report.Address = %q, want %q", report.Address, "https://node-address:8081")
	}
	if len(report.Commands) != 0 {
		t.Fatalf("len(report.Commands) = %d, want 0", len(report.Commands))
	}

	var stored model.Node
	if err := database.First(&stored, "id = ?", "node-a").Error; err != nil {
		t.Fatalf("First(node) error = %v", err)
	}
	if stored.Address != "https://node-address:8081" {
		t.Fatalf("stored.Address = %q, want %q", stored.Address, "https://node-address:8081")
	}
	if stored.LastSeen == nil {
		t.Fatal("stored.LastSeen = nil, want timestamp")
	}
}

func TestNodeServiceReportAvailabilityIncludesPendingCommands(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "node-report-commands.db"),
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
	if err := database.Create(&model.NodeCommand{
		NodeID:  "node-a",
		Type:    model.NodeCommandTypeRefreshState,
		Status:  model.NodeCommandStatusPending,
		Payload: []byte(`{"reason":"startup"}`),
	}).Error; err != nil {
		t.Fatalf("Create(pending command) error = %v", err)
	}
	if err := database.Create(&model.NodeCommand{
		NodeID:  "node-a",
		Type:    model.NodeCommandTypeScanReplica,
		Status:  model.NodeCommandStatusCompleted,
		Payload: []byte(`{"replica_id":1}`),
	}).Error; err != nil {
		t.Fatalf("Create(completed command) error = %v", err)
	}

	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))

	report, err := nodeService.ReportAvailability("node-a", "https://node-address:8081")
	if err != nil {
		t.Fatalf("ReportAvailability() error = %v", err)
	}
	if len(report.Commands) != 1 {
		t.Fatalf("len(report.Commands) = %d, want 1", len(report.Commands))
	}
	if report.Commands[0].Type != string(model.NodeCommandTypeRefreshState) {
		t.Fatalf("report.Commands[0].Type = %q, want %q", report.Commands[0].Type, model.NodeCommandTypeRefreshState)
	}
	if report.Commands[0].Status != string(model.NodeCommandStatusPending) {
		t.Fatalf("report.Commands[0].Status = %q, want %q", report.Commands[0].Status, model.NodeCommandStatusPending)
	}
}

func TestNodeServiceCompleteCommandIsIdempotentAndScopedToNode(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "node-complete-command.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))

	if err := database.Create(&model.Node{ID: "node-a", Status: model.NodeStatusOffline, Secret: "ignored"}).Error; err != nil {
		t.Fatalf("Create(node-a) error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-b", Status: model.NodeStatusOffline, Secret: "ignored"}).Error; err != nil {
		t.Fatalf("Create(node-b) error = %v", err)
	}
	command := &model.NodeCommand{
		NodeID:  "node-a",
		Type:    model.NodeCommandTypeRefreshState,
		Status:  model.NodeCommandStatusPending,
		Payload: []byte(`{"placeholder":true}`),
	}
	if err := database.Create(command).Error; err != nil {
		t.Fatalf("Create(command) error = %v", err)
	}

	completed, err := nodeService.CompleteCommand("node-a", command.ID)
	if err != nil {
		t.Fatalf("CompleteCommand() error = %v", err)
	}
	if completed.Status != string(model.NodeCommandStatusCompleted) {
		t.Fatalf("completed.Status = %q, want %q", completed.Status, model.NodeCommandStatusCompleted)
	}

	completedAgain, err := nodeService.CompleteCommand("node-a", command.ID)
	if err != nil {
		t.Fatalf("CompleteCommand(idempotent) error = %v", err)
	}
	if completedAgain.Status != string(model.NodeCommandStatusCompleted) {
		t.Fatalf("completedAgain.Status = %q, want %q", completedAgain.Status, model.NodeCommandStatusCompleted)
	}

	if _, err := nodeService.CompleteCommand("node-b", command.ID); err != ErrNodeCommandOwnership {
		t.Fatalf("CompleteCommand(other node) error = %v, want %v", err, ErrNodeCommandOwnership)
	}
}

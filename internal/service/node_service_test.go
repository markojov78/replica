package service

import (
	"errors"
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

	node, err := nodeService.Create("node-a", "plain-secret", "http://node-a:8081", nil, true)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if node.ID != "node-a" {
		t.Fatalf("node.ID = %q, want %q", node.ID, "node-a")
	}
	if node.Status != string(model.NodeStatusOffline) {
		t.Fatalf("node.Status = %q, want %q", node.Status, model.NodeStatusOffline)
	}
	if !node.SharingEnabled {
		t.Fatal("node.SharingEnabled = false, want true")
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
	if !stored.Sharing {
		t.Fatal("stored.Sharing = false, want true")
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

func TestNodeServiceUpdateSharingEnabled(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "node-update-sharing.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	if err := database.Create(&model.Node{
		ID:      "node-a",
		Status:  model.NodeStatusOffline,
		Secret:  "ignored",
		Sharing: true,
	}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}

	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))

	disabled := false
	node, err := nodeService.Update("node-a", UpdateNodeInput{SharingEnabled: &disabled})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if node.SharingEnabled {
		t.Fatal("node.SharingEnabled = true, want false")
	}

	var stored model.Node
	if err := database.First(&stored, "id = ?", "node-a").Error; err != nil {
		t.Fatalf("First(node) error = %v", err)
	}
	if stored.Sharing {
		t.Fatal("stored.Sharing = true, want false")
	}
}

func TestNodeServiceUpdateCreatesRefreshConfigCommand(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "node-update-refresh-config.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	if err := database.Create(&model.Node{
		ID:     "node-a",
		Status: model.NodeStatusOffline,
		Secret: "ignored",
	}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}

	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	commands, unsubscribe := nodeService.Subscribe("node-a")
	defer unsubscribe()

	address := "https://node-a.example"
	if _, err := nodeService.Update("node-a", UpdateNodeInput{Address: &address}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	var command model.Command
	if err := database.First(&command, "node_id = ? AND type = ?", "node-a", model.NodeCommandTypeRefreshConfig).Error; err != nil {
		t.Fatalf("First(refresh_config command) error = %v", err)
	}
	if command.Status != model.NodeCommandStatusPending {
		t.Fatalf("command.Status = %q, want %q", command.Status, model.NodeCommandStatusPending)
	}
	if string(command.Payload) != "{}" {
		t.Fatalf("command.Payload = %s, want {}", command.Payload)
	}

	select {
	case published := <-commands:
		if published.ID != command.ID || published.Type != string(model.NodeCommandTypeRefreshConfig) {
			t.Fatalf("published command = %+v, want refresh_config id %d", published, command.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published refresh_config command")
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

	report, err := nodeService.ReportAvailability("node-a", "https://node-address:8081", 60)
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
	if stored.PublicKey != "" {
		t.Fatalf("stored.PublicKey = %q, want empty", stored.PublicKey)
	}
	if stored.LastSeen == nil {
		t.Fatal("stored.LastSeen = nil, want timestamp")
	}
	if stored.Interval == nil || *stored.Interval != 60 {
		t.Fatalf("stored.Interval = %v, want 60", stored.Interval)
	}
	if stored.Status != model.NodeStatusUnreachable {
		t.Fatalf("stored.Status = %q, want %q", stored.Status, model.NodeStatusUnreachable)
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
	if err := database.Create(&model.Command{
		NodeID:  "node-a",
		Type:    model.NodeCommandTypeRefreshState,
		Status:  model.NodeCommandStatusPending,
		Payload: []byte(`{"reason":"startup"}`),
	}).Error; err != nil {
		t.Fatalf("Create(pending command) error = %v", err)
	}
	if err := database.Create(&model.Command{
		NodeID:  "node-a",
		Type:    model.NodeCommandTypeScanReplica,
		Status:  model.NodeCommandStatusCompleted,
		Payload: []byte(`{"replica_id":1}`),
	}).Error; err != nil {
		t.Fatalf("Create(completed command) error = %v", err)
	}

	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))

	report, err := nodeService.ReportAvailability("node-a", "https://node-address:8081", 60)
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

func TestNodeServiceReconcilesAutomaticStatuses(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "node-statuses.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	now := time.Now().UTC()
	interval := float64(60)
	recent := now.Add(-2 * time.Minute)
	old := now.Add(-2*time.Minute - time.Second)
	nodes := []model.Node{
		{ID: "missing", Status: model.NodeStatusOnline, Secret: "ignored"},
		{ID: "recent", Status: model.NodeStatusOffline, Secret: "ignored", Interval: &interval, LastSeen: &recent},
		{ID: "old", Status: model.NodeStatusOnline, Secret: "ignored", Interval: &interval, LastSeen: &old},
		{ID: "disabled", Status: model.NodeStatusDisabled, Secret: "ignored", Interval: &interval, LastSeen: &recent},
		{ID: "revoked", Status: model.NodeStatusRevoked, Secret: "ignored", Interval: &interval, LastSeen: &recent},
	}
	if err := database.Create(&nodes).Error; err != nil {
		t.Fatalf("Create(nodes) error = %v", err)
	}

	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	if err := nodeService.ReconcileStatuses(now); err != nil {
		t.Fatalf("ReconcileStatuses() error = %v", err)
	}

	want := map[string]model.NodeStatus{
		"missing":  model.NodeStatusOffline,
		"recent":   model.NodeStatusUnreachable,
		"old":      model.NodeStatusOffline,
		"disabled": model.NodeStatusDisabled,
		"revoked":  model.NodeStatusRevoked,
	}
	for id, status := range want {
		var node model.Node
		if err := database.First(&node, "id = ?", id).Error; err != nil {
			t.Fatalf("First(%s) error = %v", id, err)
		}
		if node.Status != status {
			t.Fatalf("%s status = %q, want %q", id, node.Status, status)
		}
	}
}

func TestNodeServiceTracksMultipleWebSocketConnections(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "node-websockets.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	interval := float64(60)
	lastSeen := time.Now().UTC()
	if err := database.Create(&model.Node{
		ID: "node-a", Status: model.NodeStatusUnreachable, Secret: "ignored", Interval: &interval, LastSeen: &lastSeen,
	}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}

	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	if err := nodeService.WebSocketConnected("node-a"); err != nil {
		t.Fatalf("WebSocketConnected(first) error = %v", err)
	}
	if err := nodeService.WebSocketConnected("node-a"); err != nil {
		t.Fatalf("WebSocketConnected(second) error = %v", err)
	}
	assertStoredNodeStatus(t, database, "node-a", model.NodeStatusOnline)

	if err := nodeService.WebSocketDisconnected("node-a"); err != nil {
		t.Fatalf("WebSocketDisconnected(first) error = %v", err)
	}
	assertStoredNodeStatus(t, database, "node-a", model.NodeStatusOnline)

	if err := nodeService.WebSocketDisconnected("node-a"); err != nil {
		t.Fatalf("WebSocketDisconnected(second) error = %v", err)
	}
	assertStoredNodeStatus(t, database, "node-a", model.NodeStatusUnreachable)
}

func TestNodeServiceRestrictsAdminStatusTransitions(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "node-admin-statuses.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	if err := database.Create(&model.Node{ID: "node-a", Status: model.NodeStatusOnline, Secret: "ignored"}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))

	offline := string(model.NodeStatusOffline)
	if _, err := nodeService.Update("node-a", UpdateNodeInput{Status: &offline}); err != ErrInvalidNodeStatus {
		t.Fatalf("Update(online to offline) error = %v, want %v", err, ErrInvalidNodeStatus)
	}

	disabled := string(model.NodeStatusDisabled)
	if _, err := nodeService.Update("node-a", UpdateNodeInput{Status: &disabled}); err != nil {
		t.Fatalf("Update(online to disabled) error = %v", err)
	}
	if _, err := nodeService.Update("node-a", UpdateNodeInput{Status: &offline}); err != nil {
		t.Fatalf("Update(disabled to offline) error = %v", err)
	}

	online := string(model.NodeStatusOnline)
	if _, err := nodeService.Update("node-a", UpdateNodeInput{Status: &online}); err != ErrInvalidNodeStatus {
		t.Fatalf("Update(offline to online) error = %v, want %v", err, ErrInvalidNodeStatus)
	}
}

func assertStoredNodeStatus(t *testing.T, database *gorm.DB, id string, want model.NodeStatus) {
	t.Helper()
	var node model.Node
	if err := database.First(&node, "id = ?", id).Error; err != nil {
		t.Fatalf("First(%s) error = %v", id, err)
	}
	if node.Status != want {
		t.Fatalf("%s status = %q, want %q", id, node.Status, want)
	}
}

func TestNodeServiceUpdateCommandSetsStatusErrorAndScopesToNode(t *testing.T) {
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
	command := &model.Command{
		NodeID:  "node-a",
		Type:    model.NodeCommandTypeRefreshState,
		Status:  model.NodeCommandStatusPending,
		Payload: []byte(`{"placeholder":true}`),
	}
	if err := database.Create(command).Error; err != nil {
		t.Fatalf("Create(command) error = %v", err)
	}

	completed, err := nodeService.UpdateCommand("node-a", command.ID, UpdateNodeCommandInput{
		Status: string(model.NodeCommandStatusCompleted),
	})
	if err != nil {
		t.Fatalf("UpdateCommand(completed) error = %v", err)
	}
	if completed.Status != string(model.NodeCommandStatusCompleted) {
		t.Fatalf("completed.Status = %q, want %q", completed.Status, model.NodeCommandStatusCompleted)
	}
	if completed.LastError != nil {
		t.Fatalf("completed.LastError = %q, want nil", *completed.LastError)
	}

	failureReason := "refresh failed"
	failed, err := nodeService.UpdateCommand("node-a", command.ID, UpdateNodeCommandInput{
		Status: string(model.NodeCommandStatusFailed),
		Error:  &failureReason,
	})
	if err != nil {
		t.Fatalf("UpdateCommand(failed) error = %v", err)
	}
	if failed.Status != string(model.NodeCommandStatusFailed) {
		t.Fatalf("failed.Status = %q, want %q", failed.Status, model.NodeCommandStatusFailed)
	}
	if failed.LastError == nil || *failed.LastError != failureReason {
		t.Fatalf("failed.LastError = %v, want %q", failed.LastError, failureReason)
	}

	if _, err := nodeService.UpdateCommand("node-a", command.ID, UpdateNodeCommandInput{Status: "invalid"}); err != ErrInvalidNodeCommandStatus {
		t.Fatalf("UpdateCommand(invalid status) error = %v, want %v", err, ErrInvalidNodeCommandStatus)
	}

	if _, err := nodeService.UpdateCommand("node-b", command.ID, UpdateNodeCommandInput{Status: string(model.NodeCommandStatusCompleted)}); err != ErrNodeCommandOwnership {
		t.Fatalf("UpdateCommand(other node) error = %v, want %v", err, ErrNodeCommandOwnership)
	}
}

func TestNodeServiceAdminCommandEndpoints(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "admin-commands.db")})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	commandRepo := repository.NewNodeCommandRepository(database)
	nodeService := NewNodeService(repository.NewNodeRepository(database), commandRepo)
	lastError := "connection timeout"
	createdAt := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	commands := []model.Command{
		{NodeID: "node-a", Type: model.NodeCommandTypeScanReplica, Status: model.NodeCommandStatusFailed, Payload: []byte(`{"replica_id":1}`), CreatedAt: createdAt, UpdatedAt: createdAt, LastError: &lastError},
		{NodeID: "node-b", Type: model.NodeCommandTypeRefreshState, Status: model.NodeCommandStatusPending, Payload: []byte(`{}`), CreatedAt: createdAt.Add(time.Hour), UpdatedAt: createdAt.Add(time.Hour)},
	}
	if err := database.Create(&commands).Error; err != nil {
		t.Fatalf("Create(commands) error = %v", err)
	}

	after := createdAt.Add(-time.Minute)
	before := createdAt.Add(time.Minute)
	list, err := nodeService.ListCommands(1, 20, NodeCommandListFilter{
		NodeID: "node-a", Type: string(model.NodeCommandTypeScanReplica), Status: string(model.NodeCommandStatusFailed),
		CreatedAfter: &after, CreatedBefore: &before,
	})
	if err != nil {
		t.Fatalf("ListCommands() error = %v", err)
	}
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].ID != commands[0].ID {
		t.Fatalf("ListCommands() = %+v", list)
	}

	subscription, unsubscribe := nodeService.Subscribe("node-a")
	defer unsubscribe()
	updated, err := nodeService.UpdateCommandStatus(commands[0].ID, string(model.NodeCommandStatusPending))
	if err != nil {
		t.Fatalf("UpdateCommandStatus() error = %v", err)
	}
	if updated.Status != string(model.NodeCommandStatusPending) || updated.LastError == nil || *updated.LastError != lastError {
		t.Fatalf("UpdateCommandStatus() = %+v", updated)
	}
	select {
	case delivered := <-subscription:
		if delivered.ID != commands[0].ID || delivered.Status != string(model.NodeCommandStatusPending) {
			t.Fatalf("delivered command = %+v", delivered)
		}
	default:
		t.Fatal("requeued command was not published")
	}

	if _, err := nodeService.UpdateCommandStatus(commands[0].ID, string(model.NodeCommandStatusFailed)); !errors.Is(err, ErrInvalidNodeCommandStatusTransition) {
		t.Fatalf("UpdateCommandStatus(invalid transition) error = %v", err)
	}
	if _, err := nodeService.UpdateCommandStatus(commands[0].ID, "invalid"); !errors.Is(err, ErrInvalidNodeCommandStatus) {
		t.Fatalf("UpdateCommandStatus(invalid status) error = %v", err)
	}
}

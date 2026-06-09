package service

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"replica/internal/config"
	"replica/internal/db"
	"replica/internal/model"
	"replica/internal/repository"

	"gorm.io/gorm"
)

func TestEnsureReconcileCommandsForNode(t *testing.T) {
	tests := []struct {
		name            string
		existingStatus  model.CommandStatus
		existingPayload json.RawMessage
		wantCreated     int
		wantTotal       int64
	}{
		{name: "no command", wantCreated: 1, wantTotal: 1},
		{name: "pending command", existingStatus: model.NodeCommandStatusPending, wantCreated: 0, wantTotal: 1},
		{name: "failed command", existingStatus: model.NodeCommandStatusFailed, wantCreated: 1, wantTotal: 2},
		{name: "completed command", existingStatus: model.NodeCommandStatusCompleted, wantCreated: 1, wantTotal: 2},
		{name: "invalid pending payload", existingStatus: model.NodeCommandStatusPending, existingPayload: json.RawMessage(`{invalid`), wantCreated: 1, wantTotal: 2},
		{name: "pending command for other destination", existingStatus: model.NodeCommandStatusPending, existingPayload: json.RawMessage(`{"destination_replica_id":999}`), wantCreated: 1, wantTotal: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, svc, source, destination := newReconcileCommandTest(t)
			if tt.existingStatus != "" {
				payload := tt.existingPayload
				if len(payload) == 0 {
					payload, _ = json.Marshal(ReconcileReplicaCommandPayload{DestinationReplicaID: destination.ID})
				}
				if err := database.Create(&model.Command{
					NodeID:  destination.NodeID,
					Type:    model.NodeCommandTypeReconcileReplica,
					Status:  tt.existingStatus,
					Payload: payload,
				}).Error; err != nil {
					t.Fatalf("Create(command) error = %v", err)
				}
			}

			commands, err := svc.EnsureReconcileCommandsForNode(destination.NodeID)
			if err != nil {
				t.Fatalf("EnsureReconcileCommandsForNode() error = %v", err)
			}
			if len(commands) != tt.wantCreated {
				t.Fatalf("len(commands) = %d, want %d", len(commands), tt.wantCreated)
			}
			var total int64
			if err := database.Model(&model.Command{}).Count(&total).Error; err != nil {
				t.Fatalf("Count(commands) error = %v", err)
			}
			if total != tt.wantTotal {
				t.Fatalf("command count = %d, want %d", total, tt.wantTotal)
			}
			if tt.wantCreated == 1 {
				var payload ReconcileReplicaCommandPayload
				if err := json.Unmarshal(commands[0].Payload, &payload); err != nil {
					t.Fatalf("Unmarshal(command payload) error = %v", err)
				}
				if payload.SourceReplicaID != source.ID || payload.DestinationReplicaID != destination.ID || payload.SourceNodeAddress != source.Node.Address || payload.TransferToken == "" {
					t.Fatalf("payload = %+v, want centralized reconcile payload", payload)
				}
			}
		})
	}
}

func TestReplicaServiceUpdateClearsUpstreamReplica(t *testing.T) {
	database, svc, source, destination := newReconcileCommandTest(t)
	sourceID := source.ID
	if err := database.Model(&model.Replica{}).Where("id = ?", destination.ID).Update("upstream_replica_id", sourceID).Error; err != nil {
		t.Fatalf("set upstream replica error = %v", err)
	}

	updated, err := svc.Update(destination.ID, UpdateReplicaInput{UpstreamReplicaIDSet: true})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.UpstreamReplicaID != nil {
		t.Fatalf("updated.UpstreamReplicaID = %v, want nil", updated.UpstreamReplicaID)
	}
}

func newReconcileCommandTest(t *testing.T) (*gorm.DB, *ReplicaService, model.Replica, model.Replica) {
	t.Helper()

	database, err := db.Open(config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "reconcile.db")})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}
	nodes := []model.Node{
		{ID: "node-a", Status: model.NodeStatusOnline, Secret: "ignored", Address: "https://source.example"},
		{ID: "node-b", Status: model.NodeStatusOnline, Secret: "ignored", Address: "https://destination.example"},
	}
	if err := database.Create(&nodes).Error; err != nil {
		t.Fatalf("Create(nodes) error = %v", err)
	}
	inventory := model.Inventory{Name: "inventory", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(&inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	file := model.InventoryFile{InventoryID: inventory.ID, RelativeURI: "file.txt", Status: model.InventoryFileStatusActive, Version: 1}
	if err := database.Create(&file).Error; err != nil {
		t.Fatalf("Create(file) error = %v", err)
	}
	source := model.Replica{InventoryID: inventory.ID, NodeID: "node-a", URI: "/source", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem}
	destination := model.Replica{InventoryID: inventory.ID, NodeID: "node-b", URI: "/destination", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem}
	if err := database.Create(&source).Error; err != nil {
		t.Fatalf("Create(source) error = %v", err)
	}
	if err := database.Create(&destination).Error; err != nil {
		t.Fatalf("Create(destination) error = %v", err)
	}
	if err := database.Create(&[]model.ReplicaFile{
		{FileID: file.ID, ReplicaID: source.ID, Version: 1, Status: model.ReplicaFileStatusSynchronized},
		{FileID: file.ID, ReplicaID: destination.ID, Version: 0, Status: model.ReplicaFileStatusPending},
	}).Error; err != nil {
		t.Fatalf("Create(replica files) error = %v", err)
	}
	settings := NewSettingService(repository.NewSettingRepository(database))
	if err := settings.EnsureTransferKeys(); err != nil {
		t.Fatalf("EnsureTransferKeys() error = %v", err)
	}
	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	source.Node = nodes[0]
	return database, NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database), nodeService, settings), source, destination
}

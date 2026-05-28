package service

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"dropoutbox/internal/config"
	"dropoutbox/internal/db"
	"dropoutbox/internal/model"
	"dropoutbox/internal/repository"
)

func TestInventoryNameFromURI(t *testing.T) {
	tests := []struct {
		uri  string
		want string
	}{
		{uri: "/home/username/images/Vacation March 2026", want: "Vacation March 2026"},
		{uri: "/home/username/images/Vacation March 2026/", want: "Vacation March 2026"},
		{uri: "s3://photo-bucket/album-one", want: "album-one"},
		{uri: `C:\photos\summer`, want: "summer"},
	}

	for _, test := range tests {
		if got := inventoryNameFromURI(test.uri); got != test.want {
			t.Fatalf("inventoryNameFromURI(%q) = %q, want %q", test.uri, got, test.want)
		}
	}
}

func TestInventoryServiceCreateCreatesPendingScanReplicaCommand(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "inventory-create-command.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	svc := NewInventoryService(repository.NewInventoryRepository(database), nodeService)

	inventory, err := svc.Create(CreateInventoryInput{
		Name:   "Photos",
		Type:   string(model.InventoryTypeFolder),
		NodeID: "node-a",
		URI:    "/data/photos",
		UserID: 7,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if len(inventory.Replicas) != 1 {
		t.Fatalf("len(inventory.Replicas) = %d, want 1", len(inventory.Replicas))
	}

	var command model.Command
	if err := database.First(&command, "node_id = ? AND type = ?", "node-a", model.NodeCommandTypeScanReplica).Error; err != nil {
		t.Fatalf("First(command) error = %v", err)
	}
	if command.Status != model.NodeCommandStatusPending {
		t.Fatalf("command.Status = %q, want %q", command.Status, model.NodeCommandStatusPending)
	}

	var payload struct {
		ReplicaID uint `json:"replica_id"`
	}
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal(command.Payload) error = %v", err)
	}
	if payload.ReplicaID != inventory.Replicas[0].ID {
		t.Fatalf("payload.ReplicaID = %d, want %d", payload.ReplicaID, inventory.Replicas[0].ID)
	}
}

func TestInventoryServiceListReplicaFiles(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "replica-files-list.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	replica := &model.Replica{
		InventoryID: 1,
		NodeID:      "node-a",
		URI:         "/data",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(replica).Error; err != nil {
		t.Fatalf("Create(replica) error = %v", err)
	}
	if err := database.Create(&model.ReplicaFile{
		FileID:    10,
		ReplicaID: replica.ID,
		Version:   2,
		Status:    model.ReplicaFileStatusSynchronized,
	}).Error; err != nil {
		t.Fatalf("Create(replica_file) error = %v", err)
	}

	svc := NewInventoryService(repository.NewInventoryRepository(database))

	files, err := svc.ListReplicaFiles(replica.ID, 1, 20, ReplicaFileListFilter{})
	if err != nil {
		t.Fatalf("ListReplicaFiles() error = %v", err)
	}
	if len(files.Items) != 1 {
		t.Fatalf("len(files.Items) = %d, want 1", len(files.Items))
	}
	if files.Items[0].ReplicaID != replica.ID {
		t.Fatalf("files.Items[0].ReplicaID = %d, want %d", files.Items[0].ReplicaID, replica.ID)
	}
	if files.Items[0].FileID != 10 {
		t.Fatalf("files.Items[0].FileID = %d, want %d", files.Items[0].FileID, 10)
	}
}

func TestInventoryServiceListReplicaFilesWithFilters(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "replica-files-filter.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	replica := &model.Replica{
		InventoryID: 1,
		NodeID:      "node-a",
		URI:         "/data",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(replica).Error; err != nil {
		t.Fatalf("Create(replica) error = %v", err)
	}
	if err := database.Create(&model.ReplicaFile{
		FileID:    10,
		ReplicaID: replica.ID,
		Version:   2,
		Status:    model.ReplicaFileStatusSynchronized,
	}).Error; err != nil {
		t.Fatalf("Create(replica_file synchronized) error = %v", err)
	}
	if err := database.Create(&model.ReplicaFile{
		FileID:    11,
		ReplicaID: replica.ID,
		Version:   3,
		Status:    model.ReplicaFileStatusPending,
	}).Error; err != nil {
		t.Fatalf("Create(replica_file pending) error = %v", err)
	}

	svc := NewInventoryService(repository.NewInventoryRepository(database))

	version := uint(3)
	files, err := svc.ListReplicaFiles(replica.ID, 1, 20, ReplicaFileListFilter{
		Status:  string(model.ReplicaFileStatusPending),
		Version: &version,
	})
	if err != nil {
		t.Fatalf("ListReplicaFiles(filtered) error = %v", err)
	}
	if len(files.Items) != 1 {
		t.Fatalf("len(files.Items) = %d, want 1", len(files.Items))
	}
	if files.Items[0].FileID != 11 {
		t.Fatalf("files.Items[0].FileID = %d, want %d", files.Items[0].FileID, 11)
	}
	if files.Items[0].Status != string(model.ReplicaFileStatusPending) {
		t.Fatalf("files.Items[0].Status = %q, want %q", files.Items[0].Status, model.ReplicaFileStatusPending)
	}
}

func TestInventoryServiceListReplicaFilesRejectsInvalidStatus(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "replica-files-invalid-status.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	replica := &model.Replica{
		InventoryID: 1,
		NodeID:      "node-a",
		URI:         "/data",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(replica).Error; err != nil {
		t.Fatalf("Create(replica) error = %v", err)
	}

	svc := NewInventoryService(repository.NewInventoryRepository(database))

	if _, err := svc.ListReplicaFiles(replica.ID, 1, 20, ReplicaFileListFilter{Status: "invalid"}); err != ErrInvalidReplicaFileStatus {
		t.Fatalf("ListReplicaFiles(invalid status) error = %v, want %v", err, ErrInvalidReplicaFileStatus)
	}
}

func TestInventoryServiceGetReplicaFile(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "replica-file-get.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	replica := &model.Replica{
		InventoryID: 1,
		NodeID:      "node-a",
		URI:         "/data",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(replica).Error; err != nil {
		t.Fatalf("Create(replica) error = %v", err)
	}
	replicaFile := &model.ReplicaFile{
		FileID:    10,
		ReplicaID: replica.ID,
		Version:   2,
		Status:    model.ReplicaFileStatusSynchronized,
	}
	if err := database.Create(replicaFile).Error; err != nil {
		t.Fatalf("Create(replica_file) error = %v", err)
	}

	svc := NewInventoryService(repository.NewInventoryRepository(database))

	file, err := svc.GetReplicaFile(replica.ID, replicaFile.FileID)
	if err != nil {
		t.Fatalf("GetReplicaFile() error = %v", err)
	}
	if file.ID != replicaFile.FileID {
		t.Fatalf("file.ID = %d, want %d", file.ID, replicaFile.FileID)
	}
	if file.Status != string(model.ReplicaFileStatusSynchronized) {
		t.Fatalf("file.Status = %q, want %q", file.Status, model.ReplicaFileStatusSynchronized)
	}
}

func TestInventoryServiceGetReplicaFileNotFound(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "replica-file-not-found.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	replica := &model.Replica{
		InventoryID: 1,
		NodeID:      "node-a",
		URI:         "/data",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(replica).Error; err != nil {
		t.Fatalf("Create(replica) error = %v", err)
	}

	svc := NewInventoryService(repository.NewInventoryRepository(database))

	if _, err := svc.GetReplicaFile(replica.ID, 999); err != ErrReplicaFileNotFound {
		t.Fatalf("GetReplicaFile() error = %v, want %v", err, ErrReplicaFileNotFound)
	}
}

func TestInventoryServiceReportReplicaFileChanges(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "replica-file-report.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	inventory := &model.Inventory{
		Name:   "photos",
		Status: model.InventoryStatusActive,
		Type:   model.InventoryTypeFolder,
	}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}

	file := &model.InventoryFile{
		InventoryID: inventory.ID,
		RelativeURI: "album/img.jpg",
		Status:      model.InventoryFileStatusActive,
		Size:        100,
		Hash:        "old-hash",
		Version:     3,
		Created:     time.Now().UTC().Add(-time.Hour),
		Modified:    time.Now().UTC().Add(-time.Minute),
	}
	if err := database.Create(file).Error; err != nil {
		t.Fatalf("Create(file) error = %v", err)
	}

	replicaA := &model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-a",
		URI:         "/data/a",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	replicaB := &model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-b",
		URI:         "/data/b",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(replicaA).Error; err != nil {
		t.Fatalf("Create(replicaA) error = %v", err)
	}
	if err := database.Create(replicaB).Error; err != nil {
		t.Fatalf("Create(replicaB) error = %v", err)
	}

	if err := database.Create(&model.ReplicaFile{
		FileID:    file.ID,
		ReplicaID: replicaA.ID,
		Version:   3,
		Status:    model.ReplicaFileStatusSynchronized,
	}).Error; err != nil {
		t.Fatalf("Create(replicaFileA) error = %v", err)
	}
	if err := database.Create(&model.ReplicaFile{
		FileID:    file.ID,
		ReplicaID: replicaB.ID,
		Version:   3,
		Status:    model.ReplicaFileStatusSynchronized,
	}).Error; err != nil {
		t.Fatalf("Create(replicaFileB) error = %v", err)
	}

	svc := NewInventoryService(repository.NewInventoryRepository(database))
	modified := time.Now().UTC().Truncate(time.Second)

	if err := svc.ReportReplicaFileChanges(replicaA.ID, "node-a", []ReplicaFileChangeInput{
		{
			FileID:       file.ID,
			FileSize:     200,
			FileHash:     "new-hash",
			ModifiedTime: modified,
		},
	}); err != nil {
		t.Fatalf("ReportReplicaFileChanges() error = %v", err)
	}

	var updatedFile model.InventoryFile
	if err := database.First(&updatedFile, file.ID).Error; err != nil {
		t.Fatalf("First(updatedFile) error = %v", err)
	}
	if updatedFile.Version != 4 {
		t.Fatalf("updatedFile.Version = %d, want 4", updatedFile.Version)
	}
	if updatedFile.Size != 200 {
		t.Fatalf("updatedFile.Size = %d, want 200", updatedFile.Size)
	}
	if updatedFile.Hash != "new-hash" {
		t.Fatalf("updatedFile.Hash = %q, want %q", updatedFile.Hash, "new-hash")
	}

	var journal model.FileJournal
	if err := database.First(&journal).Error; err != nil {
		t.Fatalf("First(journal) error = %v", err)
	}
	if journal.FileID != file.ID || journal.ReplicaID != replicaA.ID || journal.Version != 3 {
		t.Fatalf("journal = %+v, want file_id=%d replica_id=%d version=3", journal, file.ID, replicaA.ID)
	}
	if journal.Action != model.FileJournalActionUpdated {
		t.Fatalf("journal.Action = %q, want %q", journal.Action, model.FileJournalActionUpdated)
	}

	var updatedReplicaA model.ReplicaFile
	if err := database.Where("file_id = ? AND replica_id = ?", file.ID, replicaA.ID).First(&updatedReplicaA).Error; err != nil {
		t.Fatalf("First(updatedReplicaA) error = %v", err)
	}
	if updatedReplicaA.Version != 4 {
		t.Fatalf("updatedReplicaA.Version = %d, want 4", updatedReplicaA.Version)
	}
	if updatedReplicaA.Status != model.ReplicaFileStatusSynchronized {
		t.Fatalf("updatedReplicaA.Status = %q, want %q", updatedReplicaA.Status, model.ReplicaFileStatusSynchronized)
	}

	var updatedReplicaB model.ReplicaFile
	if err := database.Where("file_id = ? AND replica_id = ?", file.ID, replicaB.ID).First(&updatedReplicaB).Error; err != nil {
		t.Fatalf("First(updatedReplicaB) error = %v", err)
	}
	if updatedReplicaB.Version != 3 {
		t.Fatalf("updatedReplicaB.Version = %d, want 3", updatedReplicaB.Version)
	}
	if updatedReplicaB.Status != model.ReplicaFileStatusPending {
		t.Fatalf("updatedReplicaB.Status = %q, want %q", updatedReplicaB.Status, model.ReplicaFileStatusPending)
	}
}

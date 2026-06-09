package service

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"replica/internal/config"
	"replica/internal/db"
	"replica/internal/model"
	"replica/internal/repository"
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

func TestInventoryServiceCreateReplicaPopulatesPendingFilesAndCommand(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "replica-create-command.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	inventory := &model.Inventory{
		Name:   "Photos",
		Status: model.InventoryStatusActive,
		Type:   model.InventoryTypeFolder,
	}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	if err := database.Create(&[]model.Node{
		{ID: "node-a", Status: model.NodeStatusOffline, Secret: "secret", Address: "https://node-a.example"},
		{ID: "node-b", Status: model.NodeStatusOffline, Secret: "secret", Address: "https://node-b.example"},
	}).Error; err != nil {
		t.Fatalf("Create(nodes) error = %v", err)
	}

	files := []model.InventoryFile{
		{
			InventoryID: inventory.ID,
			RelativeURI: "one.jpg",
			Status:      model.InventoryFileStatusActive,
			Size:        100,
			Hash:        "hash-one",
			Version:     3,
			Created:     time.Now().UTC(),
			Modified:    time.Now().UTC(),
		},
		{
			InventoryID: inventory.ID,
			RelativeURI: "two.jpg",
			Status:      model.InventoryFileStatusActive,
			Size:        200,
			Hash:        "hash-two",
			Version:     4,
			Created:     time.Now().UTC(),
			Modified:    time.Now().UTC(),
		},
	}
	if err := database.Create(&files).Error; err != nil {
		t.Fatalf("Create(files) error = %v", err)
	}
	sourceReplica := model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-a",
		URI:         "/data/photos",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(&sourceReplica).Error; err != nil {
		t.Fatalf("Create(sourceReplica) error = %v", err)
	}
	if err := database.Create(&[]model.ReplicaFile{
		{FileID: files[0].ID, ReplicaID: sourceReplica.ID, Version: 3, Status: model.ReplicaFileStatusSynchronized},
		{FileID: files[1].ID, ReplicaID: sourceReplica.ID, Version: 4, Status: model.ReplicaFileStatusSynchronized},
	}).Error; err != nil {
		t.Fatalf("Create(sourceReplicaFiles) error = %v", err)
	}
	sameNodeSourceReplica := model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-b",
		URI:         "/data/local-cache",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(&sameNodeSourceReplica).Error; err != nil {
		t.Fatalf("Create(sameNodeSourceReplica) error = %v", err)
	}
	if err := database.Create(&[]model.ReplicaFile{
		{FileID: files[0].ID, ReplicaID: sameNodeSourceReplica.ID, Version: 3, Status: model.ReplicaFileStatusSynchronized},
		{FileID: files[1].ID, ReplicaID: sameNodeSourceReplica.ID, Version: 4, Status: model.ReplicaFileStatusSynchronized},
	}).Error; err != nil {
		t.Fatalf("Create(sameNodeSourceReplicaFiles) error = %v", err)
	}
	settingService := NewSettingService(repository.NewSettingRepository(database))
	if err := settingService.EnsureTransferKeys(); err != nil {
		t.Fatalf("EnsureTransferKeys() error = %v", err)
	}

	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	inventoryRepo := repository.NewInventoryRepository(database)
	svc := NewReplicaService(repository.NewReplicaRepository(database), inventoryRepo, nodeService, settingService)

	replica, err := svc.Create(CreateReplicaInput{
		InventoryID: inventory.ID,
		NodeID:      "node-b",
		URI:         "s3://bucket/photos",
		Type:        string(model.ReplicaTypeStorage),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	var replicaFiles []model.ReplicaFile
	if err := database.Where("replica_id = ?", replica.ID).Order("file_id asc").Find(&replicaFiles).Error; err != nil {
		t.Fatalf("Find(replicaFiles) error = %v", err)
	}
	if len(replicaFiles) != len(files) {
		t.Fatalf("len(replicaFiles) = %d, want %d", len(replicaFiles), len(files))
	}
	for i, replicaFile := range replicaFiles {
		if replicaFile.FileID != files[i].ID {
			t.Fatalf("replicaFiles[%d].FileID = %d, want %d", i, replicaFile.FileID, files[i].ID)
		}
		if replicaFile.Version != 0 {
			t.Fatalf("replicaFiles[%d].Version = %d, want 0", i, replicaFile.Version)
		}
		if replicaFile.Status != model.ReplicaFileStatusPending {
			t.Fatalf("replicaFiles[%d].Status = %q, want %q", i, replicaFile.Status, model.ReplicaFileStatusPending)
		}
	}

	var command model.Command
	if err := database.First(&command, "node_id = ? AND type = ?", "node-b", model.NodeCommandTypeReconcileReplica).Error; err != nil {
		t.Fatalf("First(command) error = %v", err)
	}
	if command.Status != model.NodeCommandStatusPending {
		t.Fatalf("command.Status = %q, want %q", command.Status, model.NodeCommandStatusPending)
	}

	var payload ReconcileReplicaCommandPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal(command.Payload) error = %v", err)
	}
	if payload.DestinationReplicaID != replica.ID {
		t.Fatalf("payload.DestinationReplicaID = %d, want %d", payload.DestinationReplicaID, replica.ID)
	}
	if payload.SourceReplicaID != sameNodeSourceReplica.ID || payload.SourceNodeID != "node-b" || payload.SourceNodeAddress != "https://node-b.example" || payload.TransferToken == "" {
		t.Fatalf("payload = %+v, want source replica/node/address and transfer token", payload)
	}
}

func TestReplicaServiceCreateValidatesUpstreamReplica(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "replica-create-upstream.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	inventoryA := &model.Inventory{Name: "a", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	inventoryB := &model.Inventory{Name: "b", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(inventoryA).Error; err != nil {
		t.Fatalf("Create(inventoryA) error = %v", err)
	}
	if err := database.Create(inventoryB).Error; err != nil {
		t.Fatalf("Create(inventoryB) error = %v", err)
	}
	if err := database.Create(&[]model.Node{
		{ID: "node-a", Status: model.NodeStatusOffline, Secret: "secret", Address: "https://node-a.example"},
		{ID: "node-c", Status: model.NodeStatusOffline, Secret: "secret", Address: "https://node-c.example"},
	}).Error; err != nil {
		t.Fatalf("Create(nodes) error = %v", err)
	}
	upstream := &model.Replica{
		InventoryID: inventoryA.ID,
		NodeID:      "node-a",
		URI:         "/data/a",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	foreignUpstream := &model.Replica{
		InventoryID: inventoryB.ID,
		NodeID:      "node-b",
		URI:         "/data/b",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	deletedUpstream := &model.Replica{
		InventoryID: inventoryA.ID,
		NodeID:      "node-a",
		URI:         "/data/deleted",
		Status:      model.ReplicaStatusDeleted,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(upstream).Error; err != nil {
		t.Fatalf("Create(upstream) error = %v", err)
	}
	if err := database.Create(foreignUpstream).Error; err != nil {
		t.Fatalf("Create(foreignUpstream) error = %v", err)
	}
	if err := database.Create(deletedUpstream).Error; err != nil {
		t.Fatalf("Create(deletedUpstream) error = %v", err)
	}

	settingService := NewSettingService(repository.NewSettingRepository(database))
	if err := settingService.EnsureTransferKeys(); err != nil {
		t.Fatalf("EnsureTransferKeys() error = %v", err)
	}
	svc := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database), settingService)
	upstreamID := upstream.ID
	replica, err := svc.Create(CreateReplicaInput{
		InventoryID:       inventoryA.ID,
		NodeID:            "node-c",
		URI:               "/data/c",
		Type:              string(model.ReplicaTypeFilesystem),
		UpstreamReplicaID: &upstreamID,
	})
	if err != nil {
		t.Fatalf("Create(valid upstream) error = %v", err)
	}
	if replica.UpstreamReplicaID == nil || *replica.UpstreamReplicaID != upstream.ID {
		t.Fatalf("replica.UpstreamReplicaID = %v, want %d", replica.UpstreamReplicaID, upstream.ID)
	}

	foreignID := foreignUpstream.ID
	if _, err := svc.Create(CreateReplicaInput{
		InventoryID:       inventoryA.ID,
		NodeID:            "node-d",
		URI:               "/data/d",
		Type:              string(model.ReplicaTypeFilesystem),
		UpstreamReplicaID: &foreignID,
	}); err != ErrInvalidReplicaUpstream {
		t.Fatalf("Create(foreign upstream) error = %v, want %v", err, ErrInvalidReplicaUpstream)
	}

	deletedID := deletedUpstream.ID
	if _, err := svc.Create(CreateReplicaInput{
		InventoryID:       inventoryA.ID,
		NodeID:            "node-e",
		URI:               "/data/e",
		Type:              string(model.ReplicaTypeFilesystem),
		UpstreamReplicaID: &deletedID,
	}); err != ErrInvalidReplicaUpstream {
		t.Fatalf("Create(deleted upstream) error = %v, want %v", err, ErrInvalidReplicaUpstream)
	}
}

func TestReplicaServiceRejectsActiveReplicaOnDeletedInventory(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "replica-deleted-inventory.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	inventory := &model.Inventory{Name: "deleted", Status: model.InventoryStatusDeleted, Type: model.InventoryTypeFolder}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-a", Status: model.NodeStatusOffline, Secret: "secret"}).Error; err != nil {
		t.Fatalf("Create(node) error = %v", err)
	}
	replica := &model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-a",
		URI:         "/data/deleted",
		Status:      model.ReplicaStatusDeleted,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(replica).Error; err != nil {
		t.Fatalf("Create(replica) error = %v", err)
	}

	svc := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database))
	if _, err := svc.Create(CreateReplicaInput{
		InventoryID: inventory.ID,
		NodeID:      "node-a",
		URI:         "/data/new",
		Type:        string(model.ReplicaTypeFilesystem),
	}); err != ErrInventoryDeleted {
		t.Fatalf("Create() error = %v, want %v", err, ErrInventoryDeleted)
	}

	active := string(model.ReplicaStatusActive)
	if _, err := svc.Update(replica.ID, UpdateReplicaInput{Status: &active}); err != ErrInventoryDeleted {
		t.Fatalf("Update(enable) error = %v, want %v", err, ErrInventoryDeleted)
	}

	deleted := string(model.ReplicaStatusDeleted)
	if _, err := svc.Update(replica.ID, UpdateReplicaInput{Status: &deleted}); err != nil {
		t.Fatalf("Update(keep deleted) error = %v", err)
	}
}

func TestReplicaServiceRejectsUndeleteWhenActiveReplicaUsesLocation(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "replica-active-location.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	inventory := &model.Inventory{Name: "active", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	replicas := []model.Replica{
		{InventoryID: inventory.ID, NodeID: "node-a", URI: "/data/shared", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem},
		{InventoryID: inventory.ID, NodeID: "node-a", URI: "/data/shared", Status: model.ReplicaStatusDeleted, Type: model.ReplicaTypeFilesystem},
		{InventoryID: inventory.ID, NodeID: "node-b", URI: "/data/shared", Status: model.ReplicaStatusDeleted, Type: model.ReplicaTypeFilesystem},
	}
	if err := database.Create(&replicas).Error; err != nil {
		t.Fatalf("Create(replicas) error = %v", err)
	}

	svc := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database))
	active := string(model.ReplicaStatusActive)
	_, err = svc.Update(replicas[1].ID, UpdateReplicaInput{Status: &active})
	var conflict *ActiveReplicaLocationError
	if !errors.As(err, &conflict) {
		t.Fatalf("Update(conflict) error = %v, want ActiveReplicaLocationError", err)
	}
	wantMessage := "Active replica 1 on node-a is already using location /data/shared"
	if conflict.Error() != wantMessage {
		t.Fatalf("conflict.Error() = %q, want %q", conflict.Error(), wantMessage)
	}

	if _, err := svc.Update(replicas[2].ID, UpdateReplicaInput{Status: &active}); err != nil {
		t.Fatalf("Update(different node) error = %v", err)
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

	svc := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database))

	files, err := svc.ListFiles(replica.ID, 1, 20, ReplicaFileListFilter{})
	if err != nil {
		t.Fatalf("ListFiles() error = %v", err)
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

	svc := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database))

	version := uint(3)
	files, err := svc.ListFiles(replica.ID, 1, 20, ReplicaFileListFilter{
		Status:  string(model.ReplicaFileStatusPending),
		Version: &version,
	})
	if err != nil {
		t.Fatalf("ListFiles(filtered) error = %v", err)
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

	svc := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database))

	if _, err := svc.ListFiles(replica.ID, 1, 20, ReplicaFileListFilter{Status: "invalid"}); err != ErrInvalidReplicaFileStatus {
		t.Fatalf("ListFiles(invalid status) error = %v, want %v", err, ErrInvalidReplicaFileStatus)
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

	svc := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database))

	file, err := svc.GetFile(replica.ID, replicaFile.FileID)
	if err != nil {
		t.Fatalf("GetFile() error = %v", err)
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

	svc := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database))

	if _, err := svc.GetFile(replica.ID, 999); err != ErrReplicaFileNotFound {
		t.Fatalf("GetFile() error = %v, want %v", err, ErrReplicaFileNotFound)
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
	if err := database.Create(&model.Node{ID: "node-a", Status: model.NodeStatusOnline, Address: "https://node-a.example"}).Error; err != nil {
		t.Fatalf("Create(node-a) error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-b", Status: model.NodeStatusOnline, Address: "https://node-b.example"}).Error; err != nil {
		t.Fatalf("Create(node-b) error = %v", err)
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

	settingService := NewSettingService(repository.NewSettingRepository(database))
	if err := settingService.EnsureTransferKeys(); err != nil {
		t.Fatalf("EnsureTransferKeys() error = %v", err)
	}
	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	svc := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database), nodeService, settingService)
	fileID := file.ID
	created := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	modified := time.Now().UTC().Truncate(time.Second)

	if err := svc.ReportFileChanges(replicaA.ID, "node-a", []ReplicaFileChangeInput{
		{
			FileID:       &fileID,
			RelativeURI:  "album/img.jpg",
			FileSize:     200,
			FileHash:     "new-hash",
			CreatedTime:  created,
			ModifiedTime: modified,
		},
	}); err != nil {
		t.Fatalf("ReportFileChanges() error = %v", err)
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
	if !updatedFile.Created.Equal(created) {
		t.Fatalf("updatedFile.Created = %s, want %s", updatedFile.Created, created)
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

	var command model.Command
	if err := database.Where("node_id = ? AND type = ?", "node-b", model.NodeCommandTypeReconcileReplica).First(&command).Error; err != nil {
		t.Fatalf("First(command) error = %v", err)
	}
	var payload ReconcileReplicaCommandPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal(command.Payload) error = %v", err)
	}
	if payload.SourceReplicaID != replicaA.ID || payload.DestinationReplicaID != replicaB.ID {
		t.Fatalf("payload source/destination = %d/%d, want %d/%d", payload.SourceReplicaID, payload.DestinationReplicaID, replicaA.ID, replicaB.ID)
	}
	if payload.SourceNodeID != "node-a" || payload.SourceNodeAddress != "https://node-a.example" || payload.TransferToken == "" {
		t.Fatalf("payload = %+v, want node-a source with transfer token", payload)
	}
}

func TestInventoryServiceReportReplicaFileChangesCreatesNewFile(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "replica-file-report-create.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	inventory := &model.Inventory{Name: "photos", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-a", Status: model.NodeStatusOnline, Address: "https://node-a.example"}).Error; err != nil {
		t.Fatalf("Create(node-a) error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-b", Status: model.NodeStatusOnline, Address: "https://node-b.example"}).Error; err != nil {
		t.Fatalf("Create(node-b) error = %v", err)
	}
	replica := &model.Replica{
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
	if err := database.Create(replica).Error; err != nil {
		t.Fatalf("Create(replica) error = %v", err)
	}
	if err := database.Create(replicaB).Error; err != nil {
		t.Fatalf("Create(replicaB) error = %v", err)
	}

	settingService := NewSettingService(repository.NewSettingRepository(database))
	if err := settingService.EnsureTransferKeys(); err != nil {
		t.Fatalf("EnsureTransferKeys() error = %v", err)
	}
	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	svc := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database), nodeService, settingService)
	created := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	modified := time.Now().UTC().Truncate(time.Second)

	if err := svc.ReportFileChanges(replica.ID, "node-a", []ReplicaFileChangeInput{
		{
			RelativeURI:  "album/new.jpg",
			FileSize:     123,
			FileHash:     "new-hash",
			CreatedTime:  created,
			ModifiedTime: modified,
		},
	}); err != nil {
		t.Fatalf("ReportFileChanges() error = %v", err)
	}

	var file model.InventoryFile
	if err := database.Where("inventory_id = ? AND relative_uri = ?", inventory.ID, "album/new.jpg").First(&file).Error; err != nil {
		t.Fatalf("First(file) error = %v", err)
	}
	if file.Version != 1 || file.Status != model.InventoryFileStatusActive || file.Size != 123 || file.Hash != "new-hash" {
		t.Fatalf("file = %+v, want version=1 active size=123 hash=new-hash", file)
	}

	var replicaFile model.ReplicaFile
	if err := database.Where("file_id = ? AND replica_id = ?", file.ID, replica.ID).First(&replicaFile).Error; err != nil {
		t.Fatalf("First(replicaFile) error = %v", err)
	}
	if replicaFile.Version != 1 || replicaFile.Status != model.ReplicaFileStatusSynchronized {
		t.Fatalf("replicaFile = %+v, want version=1 synchronized", replicaFile)
	}

	var pendingReplicaFile model.ReplicaFile
	if err := database.Where("file_id = ? AND replica_id = ?", file.ID, replicaB.ID).First(&pendingReplicaFile).Error; err != nil {
		t.Fatalf("First(pendingReplicaFile) error = %v", err)
	}
	if pendingReplicaFile.Version != 0 || pendingReplicaFile.Status != model.ReplicaFileStatusPending {
		t.Fatalf("pendingReplicaFile = %+v, want version=0 pending", pendingReplicaFile)
	}

	var command model.Command
	if err := database.Where("node_id = ? AND type = ?", "node-b", model.NodeCommandTypeReconcileReplica).First(&command).Error; err != nil {
		t.Fatalf("First(command) error = %v", err)
	}
	var payload ReconcileReplicaCommandPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal(command.Payload) error = %v", err)
	}
	if payload.SourceReplicaID != replica.ID || payload.DestinationReplicaID != replicaB.ID || payload.TransferToken == "" {
		t.Fatalf("payload = %+v, want source=%d destination=%d with token", payload, replica.ID, replicaB.ID)
	}

	var journal model.FileJournal
	if err := database.First(&journal).Error; err != nil {
		t.Fatalf("First(journal) error = %v", err)
	}
	if journal.FileID != file.ID || journal.Version != 0 || journal.Action != model.FileJournalActionCreated {
		t.Fatalf("journal = %+v, want file_id=%d version=0 action=created", journal, file.ID)
	}
}

func TestInventoryServiceReportReplicaFileChangesIgnoresNoContentChange(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "replica-file-report-no-content.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	inventory := &model.Inventory{Name: "photos", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	file := &model.InventoryFile{
		InventoryID: inventory.ID,
		RelativeURI: "album/same.jpg",
		Status:      model.InventoryFileStatusActive,
		Size:        123,
		Hash:        "same-hash",
		Version:     3,
		Created:     time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC),
		Modified:    time.Date(2026, 5, 21, 11, 0, 0, 0, time.UTC),
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
		Version:   2,
		Status:    model.ReplicaFileStatusPending,
	}).Error; err != nil {
		t.Fatalf("Create(replicaFileB) error = %v", err)
	}

	svc := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database))
	if err := svc.ReportFileChanges(replicaB.ID, "node-b", []ReplicaFileChangeInput{
		{
			RelativeURI:  "album/same.jpg",
			FileSize:     123,
			FileHash:     "same-hash",
			CreatedTime:  time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC),
			ModifiedTime: time.Date(2026, 5, 30, 11, 0, 0, 0, time.UTC),
		},
	}); err != nil {
		t.Fatalf("ReportFileChanges() error = %v", err)
	}

	var updatedFile model.InventoryFile
	if err := database.First(&updatedFile, file.ID).Error; err != nil {
		t.Fatalf("First(updatedFile) error = %v", err)
	}
	if updatedFile.Version != 3 || updatedFile.Size != 123 || updatedFile.Hash != "same-hash" {
		t.Fatalf("updatedFile = %+v, want unchanged version=3 size=123 hash=same-hash", updatedFile)
	}

	var replicaFileB model.ReplicaFile
	if err := database.Where("file_id = ? AND replica_id = ?", file.ID, replicaB.ID).First(&replicaFileB).Error; err != nil {
		t.Fatalf("First(replicaFileB) error = %v", err)
	}
	if replicaFileB.Version != 3 || replicaFileB.Status != model.ReplicaFileStatusSynchronized {
		t.Fatalf("replicaFileB = %+v, want version=3 synchronized", replicaFileB)
	}

	var journalCount int64
	if err := database.Model(&model.FileJournal{}).Count(&journalCount).Error; err != nil {
		t.Fatalf("Count(file_journal) error = %v", err)
	}
	if journalCount != 0 {
		t.Fatalf("journalCount = %d, want 0", journalCount)
	}

	var commandCount int64
	if err := database.Model(&model.Command{}).Count(&commandCount).Error; err != nil {
		t.Fatalf("Count(commands) error = %v", err)
	}
	if commandCount != 0 {
		t.Fatalf("commandCount = %d, want 0", commandCount)
	}
}

func TestInventoryServiceReportReplicaFileChangesDeletesFile(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "replica-file-report-delete.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	inventory := &model.Inventory{Name: "photos", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-a", Status: model.NodeStatusOnline, Address: "https://node-a.example"}).Error; err != nil {
		t.Fatalf("Create(node-a) error = %v", err)
	}
	if err := database.Create(&model.Node{ID: "node-b", Status: model.NodeStatusOnline, Address: "https://node-b.example"}).Error; err != nil {
		t.Fatalf("Create(node-b) error = %v", err)
	}
	file := &model.InventoryFile{
		InventoryID: inventory.ID,
		RelativeURI: "album/delete.jpg",
		Status:      model.InventoryFileStatusActive,
		Size:        123,
		Hash:        "hash",
		Version:     3,
		Created:     time.Now().UTC().Add(-time.Hour),
		Modified:    time.Now().UTC().Add(-time.Minute),
	}
	if err := database.Create(file).Error; err != nil {
		t.Fatalf("Create(file) error = %v", err)
	}
	replicaA := &model.Replica{InventoryID: inventory.ID, NodeID: "node-a", URI: "/data/a", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem}
	replicaB := &model.Replica{InventoryID: inventory.ID, NodeID: "node-b", URI: "/data/b", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem}
	if err := database.Create(replicaA).Error; err != nil {
		t.Fatalf("Create(replicaA) error = %v", err)
	}
	if err := database.Create(replicaB).Error; err != nil {
		t.Fatalf("Create(replicaB) error = %v", err)
	}
	if err := database.Create(&model.ReplicaFile{FileID: file.ID, ReplicaID: replicaA.ID, Version: 3, Status: model.ReplicaFileStatusSynchronized}).Error; err != nil {
		t.Fatalf("Create(replicaFileA) error = %v", err)
	}
	if err := database.Create(&model.ReplicaFile{FileID: file.ID, ReplicaID: replicaB.ID, Version: 3, Status: model.ReplicaFileStatusSynchronized}).Error; err != nil {
		t.Fatalf("Create(replicaFileB) error = %v", err)
	}

	settingService := NewSettingService(repository.NewSettingRepository(database))
	if err := settingService.EnsureTransferKeys(); err != nil {
		t.Fatalf("EnsureTransferKeys() error = %v", err)
	}
	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	svc := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database), nodeService, settingService)

	fileID := file.ID
	if err := svc.ReportFileChanges(replicaA.ID, "node-a", []ReplicaFileChangeInput{
		{FileID: &fileID, Action: string(model.ReplicaFileActionDeleted), RelativeURI: "album/delete.jpg"},
	}); err != nil {
		t.Fatalf("ReportFileChanges(delete) error = %v", err)
	}

	var deletedFile model.InventoryFile
	if err := database.First(&deletedFile, file.ID).Error; err != nil {
		t.Fatalf("First(deletedFile) error = %v", err)
	}
	if deletedFile.Status != model.InventoryFileStatusDeleted || deletedFile.Version != 4 {
		t.Fatalf("deletedFile status/version = %s/%d, want deleted/4", deletedFile.Status, deletedFile.Version)
	}

	var replicaFileA model.ReplicaFile
	if err := database.Where("file_id = ? AND replica_id = ?", file.ID, replicaA.ID).First(&replicaFileA).Error; err != nil {
		t.Fatalf("First(replicaFileA) error = %v", err)
	}
	if replicaFileA.Version != 4 || replicaFileA.Status != model.ReplicaFileStatusSynchronized {
		t.Fatalf("replicaFileA = %+v, want version=4 synchronized", replicaFileA)
	}

	var replicaFileB model.ReplicaFile
	if err := database.Where("file_id = ? AND replica_id = ?", file.ID, replicaB.ID).First(&replicaFileB).Error; err != nil {
		t.Fatalf("First(replicaFileB) error = %v", err)
	}
	if replicaFileB.Version != 3 || replicaFileB.Status != model.ReplicaFileStatusPending {
		t.Fatalf("replicaFileB = %+v, want version=3 pending", replicaFileB)
	}

	var journal model.FileJournal
	if err := database.First(&journal).Error; err != nil {
		t.Fatalf("First(journal) error = %v", err)
	}
	if journal.Action != model.FileJournalActionDeleted || journal.Version != 3 || journal.ReplicaID != replicaA.ID {
		t.Fatalf("journal = %+v, want deleted version=3 replica=%d", journal, replicaA.ID)
	}

	var command model.Command
	if err := database.Where("node_id = ? AND type = ?", "node-b", model.NodeCommandTypeReconcileReplica).First(&command).Error; err != nil {
		t.Fatalf("First(command) error = %v", err)
	}
	var payload ReconcileReplicaCommandPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal(command.Payload) error = %v", err)
	}
	if payload.SourceReplicaID != replicaA.ID || payload.DestinationReplicaID != replicaB.ID || payload.TransferToken == "" {
		t.Fatalf("payload = %+v, want source=%d destination=%d with token", payload, replicaA.ID, replicaB.ID)
	}

	if err := svc.ReportFileChanges(replicaA.ID, "node-a", []ReplicaFileChangeInput{
		{FileID: &fileID, Action: string(model.ReplicaFileActionDeleted), RelativeURI: "album/delete.jpg"},
	}); err != nil {
		t.Fatalf("ReportFileChanges(repeated delete) error = %v", err)
	}

	var journalCount int64
	if err := database.Model(&model.FileJournal{}).Count(&journalCount).Error; err != nil {
		t.Fatalf("Count(journal) error = %v", err)
	}
	if journalCount != 1 {
		t.Fatalf("journalCount = %d, want 1", journalCount)
	}
	var commandCount int64
	if err := database.Model(&model.Command{}).Count(&commandCount).Error; err != nil {
		t.Fatalf("Count(command) error = %v", err)
	}
	if commandCount != 1 {
		t.Fatalf("commandCount = %d, want 1", commandCount)
	}
}

func TestReplicaServiceReportFileChangesIgnoresUnsynchronizedReplicaDelete(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "replica-file-report-unsync-delete.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	inventory := &model.Inventory{Name: "Photos", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	file := &model.InventoryFile{
		InventoryID: inventory.ID,
		RelativeURI: "album/delete.jpg",
		Status:      model.InventoryFileStatusActive,
		Size:        123,
		Hash:        "hash",
		Version:     3,
		Created:     time.Now().UTC().Add(-time.Hour),
		Modified:    time.Now().UTC().Add(-time.Minute),
	}
	if err := database.Create(file).Error; err != nil {
		t.Fatalf("Create(file) error = %v", err)
	}
	sourceReplica := &model.Replica{InventoryID: inventory.ID, NodeID: "node-a", URI: "/data/a", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem}
	pendingReplica := &model.Replica{InventoryID: inventory.ID, NodeID: "node-b", URI: "s3://bucket/photos", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeStorage}
	if err := database.Create(sourceReplica).Error; err != nil {
		t.Fatalf("Create(sourceReplica) error = %v", err)
	}
	if err := database.Create(pendingReplica).Error; err != nil {
		t.Fatalf("Create(pendingReplica) error = %v", err)
	}
	if err := database.Create(&model.ReplicaFile{FileID: file.ID, ReplicaID: sourceReplica.ID, Version: 3, Status: model.ReplicaFileStatusSynchronized}).Error; err != nil {
		t.Fatalf("Create(sourceReplicaFile) error = %v", err)
	}
	if err := database.Create(&model.ReplicaFile{FileID: file.ID, ReplicaID: pendingReplica.ID, Version: 0, Status: model.ReplicaFileStatusPending}).Error; err != nil {
		t.Fatalf("Create(pendingReplicaFile) error = %v", err)
	}

	settingService := NewSettingService(repository.NewSettingRepository(database))
	if err := settingService.EnsureTransferKeys(); err != nil {
		t.Fatalf("EnsureTransferKeys() error = %v", err)
	}
	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	svc := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database), nodeService, settingService)

	fileID := file.ID
	if err := svc.ReportFileChanges(pendingReplica.ID, "node-b", []ReplicaFileChangeInput{
		{FileID: &fileID, Action: string(model.ReplicaFileActionDeleted), RelativeURI: "album/delete.jpg"},
	}); err != nil {
		t.Fatalf("ReportFileChanges(delete) error = %v", err)
	}

	var unchangedFile model.InventoryFile
	if err := database.First(&unchangedFile, file.ID).Error; err != nil {
		t.Fatalf("First(unchangedFile) error = %v", err)
	}
	if unchangedFile.Status != model.InventoryFileStatusActive || unchangedFile.Version != 3 {
		t.Fatalf("unchangedFile status/version = %s/%d, want active/3", unchangedFile.Status, unchangedFile.Version)
	}

	var sourceReplicaFile model.ReplicaFile
	if err := database.Where("file_id = ? AND replica_id = ?", file.ID, sourceReplica.ID).First(&sourceReplicaFile).Error; err != nil {
		t.Fatalf("First(sourceReplicaFile) error = %v", err)
	}
	if sourceReplicaFile.Status != model.ReplicaFileStatusSynchronized || sourceReplicaFile.Version != 3 {
		t.Fatalf("sourceReplicaFile = %+v, want version=3 synchronized", sourceReplicaFile)
	}

	var journalCount int64
	if err := database.Model(&model.FileJournal{}).Count(&journalCount).Error; err != nil {
		t.Fatalf("Count(journal) error = %v", err)
	}
	if journalCount != 0 {
		t.Fatalf("journalCount = %d, want 0", journalCount)
	}
	var commandCount int64
	if err := database.Model(&model.Command{}).Count(&commandCount).Error; err != nil {
		t.Fatalf("Count(command) error = %v", err)
	}
	if commandCount != 0 {
		t.Fatalf("commandCount = %d, want 0", commandCount)
	}
}

func TestReplicaServiceReportFileChangesIgnoresUnsynchronizedReplicaUpdate(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "replica-file-report-unsync-update.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	inventory := &model.Inventory{Name: "Photos", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	file := &model.InventoryFile{
		InventoryID: inventory.ID,
		RelativeURI: "album/update.jpg",
		Status:      model.InventoryFileStatusActive,
		Size:        123,
		Hash:        "old-hash",
		Version:     3,
		Created:     time.Now().UTC().Add(-time.Hour),
		Modified:    time.Now().UTC().Add(-time.Minute),
	}
	if err := database.Create(file).Error; err != nil {
		t.Fatalf("Create(file) error = %v", err)
	}
	pendingReplica := &model.Replica{InventoryID: inventory.ID, NodeID: "node-b", URI: "s3://bucket/photos", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeStorage}
	if err := database.Create(pendingReplica).Error; err != nil {
		t.Fatalf("Create(pendingReplica) error = %v", err)
	}
	if err := database.Create(&model.ReplicaFile{FileID: file.ID, ReplicaID: pendingReplica.ID, Version: 0, Status: model.ReplicaFileStatusPending}).Error; err != nil {
		t.Fatalf("Create(pendingReplicaFile) error = %v", err)
	}

	settingService := NewSettingService(repository.NewSettingRepository(database))
	if err := settingService.EnsureTransferKeys(); err != nil {
		t.Fatalf("EnsureTransferKeys() error = %v", err)
	}
	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	svc := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database), nodeService, settingService)

	fileID := file.ID
	created := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	modified := time.Now().UTC().Truncate(time.Second)
	if err := svc.ReportFileChanges(pendingReplica.ID, "node-b", []ReplicaFileChangeInput{
		{
			FileID:       &fileID,
			RelativeURI:  "album/update.jpg",
			FileSize:     456,
			FileHash:     "new-hash",
			CreatedTime:  created,
			ModifiedTime: modified,
		},
	}); err != nil {
		t.Fatalf("ReportFileChanges(update) error = %v", err)
	}

	var unchangedFile model.InventoryFile
	if err := database.First(&unchangedFile, file.ID).Error; err != nil {
		t.Fatalf("First(unchangedFile) error = %v", err)
	}
	if unchangedFile.Version != 3 || unchangedFile.Size != 123 || unchangedFile.Hash != "old-hash" {
		t.Fatalf("unchangedFile version/size/hash = %d/%d/%q, want 3/123/old-hash", unchangedFile.Version, unchangedFile.Size, unchangedFile.Hash)
	}

	var journalCount int64
	if err := database.Model(&model.FileJournal{}).Count(&journalCount).Error; err != nil {
		t.Fatalf("Count(journal) error = %v", err)
	}
	if journalCount != 0 {
		t.Fatalf("journalCount = %d, want 0", journalCount)
	}
}

func TestInventoryServiceReportReplicaFileChangesRestoresDeletedFileByRelativeURI(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "replica-file-report-restore.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	inventory := &model.Inventory{Name: "photos", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	file := &model.InventoryFile{
		InventoryID: inventory.ID,
		RelativeURI: "album/restored.jpg",
		Status:      model.InventoryFileStatusDeleted,
		Size:        10,
		Hash:        "old-hash",
		Version:     5,
		Created:     time.Now().UTC().Add(-time.Hour),
		Modified:    time.Now().UTC().Add(-time.Minute),
	}
	if err := database.Create(file).Error; err != nil {
		t.Fatalf("Create(file) error = %v", err)
	}
	replica := &model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-a",
		URI:         "/data/a",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(replica).Error; err != nil {
		t.Fatalf("Create(replica) error = %v", err)
	}

	svc := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database))
	if err := svc.ReportFileChanges(replica.ID, "node-a", []ReplicaFileChangeInput{
		{
			RelativeURI:  "album/restored.jpg",
			FileSize:     20,
			FileHash:     "restored-hash",
			CreatedTime:  time.Now().UTC().Add(-time.Hour),
			ModifiedTime: time.Now().UTC(),
		},
	}); err != nil {
		t.Fatalf("ReportFileChanges() error = %v", err)
	}

	var updatedFile model.InventoryFile
	if err := database.First(&updatedFile, file.ID).Error; err != nil {
		t.Fatalf("First(updatedFile) error = %v", err)
	}
	if updatedFile.Status != model.InventoryFileStatusActive || updatedFile.Version != 6 {
		t.Fatalf("updatedFile status/version = %s/%d, want active/6", updatedFile.Status, updatedFile.Version)
	}

	var journal model.FileJournal
	if err := database.First(&journal).Error; err != nil {
		t.Fatalf("First(journal) error = %v", err)
	}
	if journal.FileID != file.ID || journal.Version != 5 || journal.Action != model.FileJournalActionRestored {
		t.Fatalf("journal = %+v, want file_id=%d version=5 action=restored", journal, file.ID)
	}
}

func TestInventoryServiceReportReplicaFileChangesRejectsInvalidFileReferences(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "replica-file-report-invalid.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	inventoryA := &model.Inventory{Name: "a", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	inventoryB := &model.Inventory{Name: "b", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(inventoryA).Error; err != nil {
		t.Fatalf("Create(inventoryA) error = %v", err)
	}
	if err := database.Create(inventoryB).Error; err != nil {
		t.Fatalf("Create(inventoryB) error = %v", err)
	}
	fileA := &model.InventoryFile{
		InventoryID: inventoryA.ID,
		RelativeURI: "same.jpg",
		Status:      model.InventoryFileStatusActive,
		Version:     1,
	}
	fileB := &model.InventoryFile{
		InventoryID: inventoryB.ID,
		RelativeURI: "foreign.jpg",
		Status:      model.InventoryFileStatusActive,
		Version:     1,
	}
	if err := database.Create(fileA).Error; err != nil {
		t.Fatalf("Create(fileA) error = %v", err)
	}
	if err := database.Create(fileB).Error; err != nil {
		t.Fatalf("Create(fileB) error = %v", err)
	}
	replica := &model.Replica{
		InventoryID: inventoryA.ID,
		NodeID:      "node-a",
		URI:         "/data/a",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(replica).Error; err != nil {
		t.Fatalf("Create(replica) error = %v", err)
	}

	svc := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database))
	now := time.Now().UTC()

	foreignID := fileB.ID
	if err := svc.ReportFileChanges(replica.ID, "node-a", []ReplicaFileChangeInput{
		{FileID: &foreignID, RelativeURI: "foreign.jpg", FileSize: 1, FileHash: "hash", CreatedTime: now, ModifiedTime: now},
	}); err != ErrInvalidReplicaFileUpdate {
		t.Fatalf("ReportFileChanges(foreign file) error = %v, want %v", err, ErrInvalidReplicaFileUpdate)
	}

	fileAID := fileA.ID
	if err := svc.ReportFileChanges(replica.ID, "node-a", []ReplicaFileChangeInput{
		{FileID: &fileAID, RelativeURI: "different.jpg", FileSize: 1, FileHash: "hash", CreatedTime: now, ModifiedTime: now},
	}); err != ErrInvalidReplicaFileUpdate {
		t.Fatalf("ReportFileChanges(uri mismatch) error = %v, want %v", err, ErrInvalidReplicaFileUpdate)
	}

	if err := svc.ReportFileChanges(replica.ID, "node-a", []ReplicaFileChangeInput{
		{RelativeURI: "same.jpg", FileSize: 1, FileHash: "hash", CreatedTime: now, ModifiedTime: now},
	}); err != ErrInvalidReplicaFileUpdate {
		t.Fatalf("ReportFileChanges(active duplicate) error = %v, want %v", err, ErrInvalidReplicaFileUpdate)
	}
}

func TestReplicaServiceReportFileChangesRepairsDownstreamReplica(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "replica-file-report-downstream.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	inventory := &model.Inventory{Name: "photos", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	if err := database.Create(&[]model.Node{
		{ID: "node-a", Status: model.NodeStatusOnline, Address: "https://node-a.example"},
		{ID: "node-b", Status: model.NodeStatusOnline, Address: "https://node-b.example"},
	}).Error; err != nil {
		t.Fatalf("Create(nodes) error = %v", err)
	}
	upstream := &model.Replica{
		InventoryID: inventory.ID,
		NodeID:      "node-a",
		URI:         "/data/a",
		Status:      model.ReplicaStatusActive,
		Type:        model.ReplicaTypeFilesystem,
	}
	if err := database.Create(upstream).Error; err != nil {
		t.Fatalf("Create(upstream) error = %v", err)
	}
	downstream := &model.Replica{
		InventoryID:       inventory.ID,
		NodeID:            "node-b",
		URI:               "/data/b",
		Status:            model.ReplicaStatusActive,
		Type:              model.ReplicaTypeFilesystem,
		UpstreamReplicaID: &upstream.ID,
	}
	if err := database.Create(downstream).Error; err != nil {
		t.Fatalf("Create(downstream) error = %v", err)
	}

	file := &model.InventoryFile{
		InventoryID: inventory.ID,
		RelativeURI: "file.txt",
		Status:      model.InventoryFileStatusActive,
		Size:        4,
		Hash:        "upstream-hash",
		Version:     7,
		Created:     time.Now().UTC().Add(-time.Hour),
		Modified:    time.Now().UTC(),
	}
	if err := database.Create(file).Error; err != nil {
		t.Fatalf("Create(file) error = %v", err)
	}
	if err := database.Create(&[]model.ReplicaFile{
		{FileID: file.ID, ReplicaID: upstream.ID, Version: 7, Status: model.ReplicaFileStatusSynchronized},
		{FileID: file.ID, ReplicaID: downstream.ID, Version: 7, Status: model.ReplicaFileStatusSynchronized},
	}).Error; err != nil {
		t.Fatalf("Create(replicaFiles) error = %v", err)
	}

	settingService := NewSettingService(repository.NewSettingRepository(database))
	if err := settingService.EnsureTransferKeys(); err != nil {
		t.Fatalf("EnsureTransferKeys() error = %v", err)
	}
	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	svc := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database), nodeService, settingService)
	now := time.Now().UTC()
	fileID := file.ID
	err = svc.ReportFileChanges(downstream.ID, "node-b", []ReplicaFileChangeInput{
		{FileID: &fileID, Action: string(model.ReplicaFileActionUpdated), RelativeURI: "file.txt", FileSizeSet: true, FileSize: 7, FileHashSet: true, FileHash: "local-hash", CreatedTimeSet: true, CreatedTime: now, ModifiedTimeSet: true, ModifiedTime: now},
		{Action: string(model.ReplicaFileActionCreated), RelativeURI: "unknown.txt", FileSizeSet: true, FileSize: 1, FileHashSet: true, FileHash: "unknown-hash", CreatedTimeSet: true, CreatedTime: now, ModifiedTimeSet: true, ModifiedTime: now},
	})
	if err != nil {
		t.Fatalf("ReportFileChanges(downstream) error = %v", err)
	}

	var unchangedFile model.InventoryFile
	if err := database.First(&unchangedFile, file.ID).Error; err != nil {
		t.Fatalf("First(inventoryFile) error = %v", err)
	}
	if unchangedFile.Version != 7 || unchangedFile.Hash != "upstream-hash" || unchangedFile.Size != 4 {
		t.Fatalf("inventoryFile = %+v, want unchanged version/content", unchangedFile)
	}
	var journalCount int64
	if err := database.Model(&model.FileJournal{}).Count(&journalCount).Error; err != nil {
		t.Fatalf("Count(fileJournal) error = %v", err)
	}
	if journalCount != 0 {
		t.Fatalf("journalCount = %d, want 0", journalCount)
	}

	var downstreamFile model.ReplicaFile
	if err := database.Where("file_id = ? AND replica_id = ?", file.ID, downstream.ID).First(&downstreamFile).Error; err != nil {
		t.Fatalf("First(downstreamFile) error = %v", err)
	}
	if downstreamFile.Status != model.ReplicaFileStatusPending || downstreamFile.Version != 7 {
		t.Fatalf("downstreamFile = %+v, want pending version 7", downstreamFile)
	}

	var command model.Command
	if err := database.Where("node_id = ? AND type = ?", "node-b", model.NodeCommandTypeReconcileReplica).First(&command).Error; err != nil {
		t.Fatalf("First(command) error = %v", err)
	}
	var payload ReconcileReplicaCommandPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal(command.Payload) error = %v", err)
	}
	if payload.SourceReplicaID != upstream.ID || payload.DestinationReplicaID != downstream.ID {
		t.Fatalf("payload source/destination = %d/%d, want %d/%d", payload.SourceReplicaID, payload.DestinationReplicaID, upstream.ID, downstream.ID)
	}
	if len(payload.DeleteRelativeURIs) != 1 || payload.DeleteRelativeURIs[0] != "unknown.txt" {
		t.Fatalf("payload.DeleteRelativeURIs = %v, want [unknown.txt]", payload.DeleteRelativeURIs)
	}

	if err := svc.ReportFileChanges(downstream.ID, "node-b", []ReplicaFileChangeInput{
		{FileID: &fileID, Action: string(model.ReplicaFileActionUpdated), RelativeURI: "file.txt", FileSizeSet: true, FileSize: 4, FileHashSet: true, FileHash: "upstream-hash", CreatedTimeSet: true, CreatedTime: now, ModifiedTimeSet: true, ModifiedTime: now},
	}); err != nil {
		t.Fatalf("ReportFileChanges(matching downstream content) error = %v", err)
	}
	var commandCount int64
	if err := database.Model(&model.Command{}).Count(&commandCount).Error; err != nil {
		t.Fatalf("Count(commands) error = %v", err)
	}
	if commandCount != 1 {
		t.Fatalf("commandCount = %d, want 1", commandCount)
	}
}

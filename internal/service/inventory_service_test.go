package service

import (
	"path/filepath"
	"testing"

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

	files, err := svc.ListReplicaFiles(replica.ID, 1, 20)
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

	file, err := svc.GetReplicaFile(replica.ID, replicaFile.ID)
	if err != nil {
		t.Fatalf("GetReplicaFile() error = %v", err)
	}
	if file.ID != replicaFile.ID {
		t.Fatalf("file.ID = %d, want %d", file.ID, replicaFile.ID)
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

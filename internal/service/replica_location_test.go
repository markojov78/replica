package service

import (
	"errors"
	"path/filepath"
	"testing"

	"replica/internal/config"
	"replica/internal/db"
	"replica/internal/model"
	"replica/internal/repository"
)

func TestReplicaLocationOverlaps(t *testing.T) {
	tests := []struct {
		name        string
		nodeA, uriA string
		nodeB, uriB string
		wantOverlap bool
	}{
		{name: "same path", nodeA: "a", uriA: "/data/photos", nodeB: "a", uriB: "/data/photos", wantOverlap: true},
		{name: "nested path", nodeA: "a", uriA: "/data/photos/album", nodeB: "a", uriB: "/data/photos", wantOverlap: true},
		{name: "file uri and path", nodeA: "a", uriA: "file:///data/photos/album", nodeB: "a", uriB: "/data/photos/", wantOverlap: true},
		{name: "sibling with common prefix", nodeA: "a", uriA: "/data/photos2", nodeB: "a", uriB: "/data/photos", wantOverlap: false},
		{name: "filesystem on other node", nodeA: "a", uriA: "/data/photos", nodeB: "b", uriB: "/data/photos", wantOverlap: false},
		{name: "windows drive", nodeA: "a", uriA: `C:\data\photos`, nodeB: "a", uriB: "file:///c:/data", wantOverlap: true},
		{name: "windows other drive", nodeA: "a", uriA: `D:\data`, nodeB: "a", uriB: `C:\data`, wantOverlap: false},
		{name: "s3 across nodes", nodeA: "a", uriA: "s3://bucket/photos/album", nodeB: "b", uriB: "s3://bucket/photos", wantOverlap: true},
		{name: "s3 bucket root", nodeA: "a", uriA: "s3://bucket", nodeB: "b", uriB: "s3://bucket/photos", wantOverlap: true},
		{name: "s3 other bucket", nodeA: "a", uriA: "s3://bucket/photos", nodeB: "a", uriB: "s3://other/photos", wantOverlap: false},
		{name: "sftp across nodes", nodeA: "a", uriA: "sftp://Host/srv/photos", nodeB: "b", uriB: "sftp://host:22/srv", wantOverlap: true},
		{name: "sftp other port", nodeA: "a", uriA: "sftp://host:2222/srv/photos", nodeB: "b", uriB: "sftp://host/srv", wantOverlap: false},
		{name: "different backends", nodeA: "a", uriA: "/srv/photos", nodeB: "a", uriB: "sftp://host/srv/photos", wantOverlap: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, ok := parseReplicaLocation(tt.nodeA, tt.uriA)
			if !ok {
				t.Fatalf("parseReplicaLocation(%q) failed", tt.uriA)
			}
			b, ok := parseReplicaLocation(tt.nodeB, tt.uriB)
			if !ok {
				t.Fatalf("parseReplicaLocation(%q) failed", tt.uriB)
			}
			if got := a.overlaps(b); got != tt.wantOverlap {
				t.Fatalf("overlaps = %t, want %t", got, tt.wantOverlap)
			}
			if got := b.overlaps(a); got != tt.wantOverlap {
				t.Fatalf("reverse overlaps = %t, want %t", got, tt.wantOverlap)
			}
		})
	}
}

func TestReplicaAndInventoryCreateRejectOverlappingLocations(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "replica-overlap.db")})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}
	for _, nodeID := range []string{"node-a", "node-b"} {
		if err := database.Create(&model.Node{ID: nodeID, Status: model.NodeStatusOnline}).Error; err != nil {
			t.Fatalf("Create(%s) error = %v", nodeID, err)
		}
	}
	inventory := &model.Inventory{Name: "photos", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	if err := database.Create(inventory).Error; err != nil {
		t.Fatalf("Create(inventory) error = %v", err)
	}
	existing := []model.Replica{
		{InventoryID: inventory.ID, NodeID: "node-a", URI: "/data/photos", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem},
		{InventoryID: inventory.ID, NodeID: "node-a", URI: "s3://bucket/photos", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeStorage},
		{InventoryID: inventory.ID, NodeID: "node-a", URI: "/data/archive", Status: model.ReplicaStatusDeleted, Type: model.ReplicaTypeFilesystem},
	}
	if err := database.Create(&existing).Error; err != nil {
		t.Fatalf("Create(replicas) error = %v", err)
	}

	replicas := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database))
	inventories := NewInventoryService(repository.NewInventoryRepository(database))
	var conflict *ActiveReplicaLocationError

	if _, err := replicas.Create(CreateReplicaInput{InventoryID: inventory.ID, NodeID: "node-a", URI: "/data/photos/album", Type: string(model.ReplicaTypeFilesystem)}); !errors.As(err, &conflict) || conflict.ReplicaID != existing[0].ID {
		t.Fatalf("Create(nested filesystem) error = %v, want conflict with replica %d", err, existing[0].ID)
	}
	if _, err := replicas.Create(CreateReplicaInput{InventoryID: inventory.ID, NodeID: "node-b", URI: "s3://bucket", Type: string(model.ReplicaTypeStorage)}); !errors.As(err, &conflict) || conflict.ReplicaID != existing[1].ID {
		t.Fatalf("Create(s3 on other node) error = %v, want conflict with replica %d", err, existing[1].ID)
	}
	folder := "/data"
	if _, err := inventories.Create(CreateInventoryInput{Name: "all", NodeID: "node-a", FolderURI: &folder, ReplicaType: string(model.ReplicaTypeFilesystem)}); !errors.As(err, &conflict) {
		t.Fatalf("inventory Create(containing folder) error = %v, want conflict", err)
	}
	// The fixture inventory has no synchronized source, so allowed creates fail later; only the overlap check matters.
	if _, err := replicas.Create(CreateReplicaInput{InventoryID: inventory.ID, NodeID: "node-b", URI: "/data/photos", Type: string(model.ReplicaTypeFilesystem)}); errors.As(err, &conflict) {
		t.Fatalf("Create(filesystem on other node) error = %v, want no overlap conflict", err)
	}
	if _, err := replicas.Create(CreateReplicaInput{InventoryID: inventory.ID, NodeID: "node-a", URI: "/data/archive/2025", Type: string(model.ReplicaTypeFilesystem)}); errors.As(err, &conflict) {
		t.Fatalf("Create(inside deleted replica) error = %v, want no overlap conflict", err)
	}
}

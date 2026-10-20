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

	"gorm.io/gorm"
)

type shareFileReportFixture struct {
	database   *gorm.DB
	svc        *ReplicaService
	replica    *model.Replica
	downstream *model.Replica
	subset     *model.Share
	whole      *model.Share
}

func newShareFileReportFixture(t *testing.T) shareFileReportFixture {
	t.Helper()

	database, err := db.Open(config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "share-file-report.db")})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}
	inventory := &model.Inventory{Name: "photos", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	mustCreate(t, database, inventory)
	mustCreate(t, database, &model.Node{ID: "node-a", Status: model.NodeStatusOnline, Address: "https://node-a.example"})
	replica := &model.Replica{InventoryID: inventory.ID, NodeID: "node-a", URI: "/data/a", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem}
	mustCreate(t, database, replica)
	downstream := &model.Replica{InventoryID: inventory.ID, NodeID: "node-a", URI: "/data/b", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem, UpstreamReplicaID: &replica.ID}
	mustCreate(t, database, downstream)

	member := &model.InventoryFile{InventoryID: inventory.ID, RelativeURI: "album/cover.jpg", Status: model.InventoryFileStatusActive, Version: 1, Size: 1, Hash: "cover"}
	mustCreate(t, database, member)
	mustCreate(t, database, &model.ReplicaFile{FileID: member.ID, ReplicaID: replica.ID, Version: 1, Status: model.ReplicaFileStatusSynchronized})
	subset := &model.Share{ReplicaID: replica.ID, Name: "subset", Status: model.ShareStatusActive}
	mustCreate(t, database, subset)
	mustCreate(t, database, &model.ShareFile{ShareID: subset.ID, FileID: member.ID})
	whole := &model.Share{ReplicaID: replica.ID, Name: "whole", Status: model.ShareStatusActive}
	mustCreate(t, database, whole)

	settingService := NewSettingService(repository.NewSettingRepository(database))
	if err := settingService.EnsureTransferKeys(); err != nil {
		t.Fatalf("EnsureTransferKeys() error = %v", err)
	}
	nodeService := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database))
	svc := NewReplicaService(repository.NewReplicaRepository(database), repository.NewInventoryRepository(database), nodeService, settingService)
	return shareFileReportFixture{database: database, svc: svc, replica: replica, downstream: downstream, subset: subset, whole: whole}
}

func mustCreate(t *testing.T, database *gorm.DB, value any) {
	t.Helper()
	if err := database.Create(value).Error; err != nil {
		t.Fatalf("Create(%T) error = %v", value, err)
	}
}

func createdShareFileChange(relativeURI, hash string, shareID *uint) ReplicaFileChangeInput {
	now := time.Now().UTC().Truncate(time.Second)
	return ReplicaFileChangeInput{
		Action:          string(model.ReplicaFileActionCreated),
		RelativeURI:     relativeURI,
		FileSize:        10,
		FileSizeSet:     true,
		FileHash:        hash,
		FileHashSet:     true,
		CreatedTime:     now,
		CreatedTimeSet:  true,
		ModifiedTime:    now,
		ModifiedTimeSet: true,
		ShareID:         shareID,
	}
}

func (f shareFileReportFixture) shareFileIDs(t *testing.T, shareID uint) []uint {
	t.Helper()
	var rows []model.ShareFile
	if err := f.database.Where("share_id = ?", shareID).Order("file_id asc").Find(&rows).Error; err != nil {
		t.Fatalf("Find(share files) error = %v", err)
	}
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.FileID)
	}
	return ids
}

func (f shareFileReportFixture) fileID(t *testing.T, relativeURI string) uint {
	t.Helper()
	var file model.InventoryFile
	if err := f.database.Where("relative_uri = ?", relativeURI).First(&file).Error; err != nil {
		t.Fatalf("First(%s) error = %v", relativeURI, err)
	}
	return file.ID
}

func TestReportFileChangesWithShareIDAddsCreatedFileToSubsetShare(t *testing.T) {
	f := newShareFileReportFixture(t)

	if err := f.svc.ReportFileChanges(f.replica.ID, "node-a", []ReplicaFileChangeInput{createdShareFileChange("album/new.jpg", "new", &f.subset.ID)}); err != nil {
		t.Fatalf("ReportFileChanges() error = %v", err)
	}

	newID := f.fileID(t, "album/new.jpg")
	if ids := f.shareFileIDs(t, f.subset.ID); len(ids) != 2 || ids[1] != newID {
		t.Fatalf("share files = %v, want cover and new file %d", ids, newID)
	}
	var refresh int64
	if err := f.database.Model(&model.Command{}).Where("node_id = ? AND type = ?", "node-a", model.NodeCommandTypeRefreshState).Count(&refresh).Error; err != nil {
		t.Fatalf("Count(refresh_state) error = %v", err)
	}
	if refresh != 1 {
		t.Fatalf("refresh_state commands = %d, want 1", refresh)
	}
}

func TestReportFileChangesWithShareIDAddsFileAlreadyReportedByWatcher(t *testing.T) {
	f := newShareFileReportFixture(t)

	if err := f.svc.ReportFileChanges(f.replica.ID, "node-a", []ReplicaFileChangeInput{createdShareFileChange("album/new.jpg", "new", nil)}); err != nil {
		t.Fatalf("watcher ReportFileChanges() error = %v", err)
	}
	if err := f.svc.ReportFileChanges(f.replica.ID, "node-a", []ReplicaFileChangeInput{createdShareFileChange("album/new.jpg", "new", &f.subset.ID)}); err != nil {
		t.Fatalf("upload ReportFileChanges() error = %v", err)
	}

	if ids := f.shareFileIDs(t, f.subset.ID); len(ids) != 2 || ids[1] != f.fileID(t, "album/new.jpg") {
		t.Fatalf("share files = %v, want the watcher-reported file added", ids)
	}
}

func TestReportFileChangesWithShareIDRestoresDeletedFileIntoShare(t *testing.T) {
	f := newShareFileReportFixture(t)
	deleted := &model.InventoryFile{InventoryID: f.replica.InventoryID, RelativeURI: "album/old.jpg", Status: model.InventoryFileStatusDeleted, Version: 2, Size: 1, Hash: "old"}
	mustCreate(t, f.database, deleted)

	if err := f.svc.ReportFileChanges(f.replica.ID, "node-a", []ReplicaFileChangeInput{createdShareFileChange("album/old.jpg", "restored", &f.subset.ID)}); err != nil {
		t.Fatalf("ReportFileChanges() error = %v", err)
	}

	if ids := f.shareFileIDs(t, f.subset.ID); len(ids) != 2 || ids[1] != deleted.ID {
		t.Fatalf("share files = %v, want restored file %d", ids, deleted.ID)
	}
}

func TestReportFileChangesWithShareIDRejectsInvalidShares(t *testing.T) {
	f := newShareFileReportFixture(t)
	other := &model.Replica{InventoryID: f.replica.InventoryID, NodeID: "node-a", URI: "/data/c", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem}
	mustCreate(t, f.database, other)
	otherShare := &model.Share{ReplicaID: other.ID, Name: "other", Status: model.ShareStatusActive}
	mustCreate(t, f.database, otherShare)
	mustCreate(t, f.database, &model.ShareFile{ShareID: otherShare.ID, FileID: f.fileID(t, "album/cover.jpg")})
	missing := uint(999999)

	tests := map[string]struct {
		replicaID uint
		change    ReplicaFileChangeInput
	}{
		"whole replica share": {f.replica.ID, createdShareFileChange("album/a.jpg", "a", &f.whole.ID)},
		"foreign replica":     {f.replica.ID, createdShareFileChange("album/b.jpg", "b", &otherShare.ID)},
		"missing share":       {f.replica.ID, createdShareFileChange("album/c.jpg", "c", &missing)},
		"downstream replica":  {f.downstream.ID, createdShareFileChange("album/d.jpg", "d", &f.subset.ID)},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := f.svc.ReportFileChanges(tt.replicaID, "node-a", []ReplicaFileChangeInput{tt.change})
			if !errors.Is(err, ErrInvalidReplicaFileUpdate) {
				t.Fatalf("ReportFileChanges() error = %v, want %v", err, ErrInvalidReplicaFileUpdate)
			}
			var count int64
			if err := f.database.Model(&model.InventoryFile{}).Where("relative_uri = ?", tt.change.RelativeURI).Count(&count).Error; err != nil {
				t.Fatalf("Count(file) error = %v", err)
			}
			if count != 0 {
				t.Fatalf("file %s was created despite rejected share", tt.change.RelativeURI)
			}
		})
	}

	updated := createdShareFileChange("album/cover.jpg", "changed", &f.subset.ID)
	updated.Action = string(model.ReplicaFileActionUpdated)
	fileID := f.fileID(t, "album/cover.jpg")
	updated.FileID = &fileID
	if err := f.svc.ReportFileChanges(f.replica.ID, "node-a", []ReplicaFileChangeInput{updated}); !errors.Is(err, ErrInvalidReplicaFileUpdate) {
		t.Fatalf("updated with share_id error = %v, want %v", err, ErrInvalidReplicaFileUpdate)
	}
}

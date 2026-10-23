package service

import (
	"reflect"
	"testing"
	"time"

	"replica/internal/metadata"
	"replica/internal/model"
)

func metadataFileChange(action string, fileID *uint, relativeURI, hash string, entries ...metadata.Entry) ReplicaFileChangeInput {
	now := time.Now().UTC().Truncate(time.Second)
	return ReplicaFileChangeInput{
		FileID:          fileID,
		Action:          action,
		RelativeURI:     relativeURI,
		FileSize:        10,
		FileSizeSet:     true,
		FileHash:        hash,
		FileHashSet:     true,
		CreatedTime:     now,
		CreatedTimeSet:  true,
		ModifiedTime:    now,
		ModifiedTimeSet: true,
		Metadata:        entries,
	}
}

func fileMetadataRows(t *testing.T, fixture shareFileReportFixture, relativeURI string) (model.InventoryFile, []model.FileMetadata) {
	t.Helper()
	var file model.InventoryFile
	if err := fixture.database.Where("relative_uri = ?", relativeURI).First(&file).Error; err != nil {
		t.Fatalf("find file error = %v", err)
	}
	var rows []model.FileMetadata
	if err := fixture.database.Where("file_id = ?", file.ID).Order("key asc").Find(&rows).Error; err != nil {
		t.Fatalf("find metadata error = %v", err)
	}
	return file, rows
}

func TestReportFileChangesStoresAndReplacesMetadata(t *testing.T) {
	fixture := newShareFileReportFixture(t)
	captured := metadata.Entry{Source: metadata.SourceEXIF, Key: metadata.KeyCaptureTime, ValueType: metadata.ValueTypeTime, Value: "2024-05-01T10:20:30"}
	width := metadata.Entry{Source: metadata.SourceImage, Key: metadata.KeyWidth, ValueType: metadata.ValueTypeInt, Value: "4000"}
	invalid := metadata.Entry{Source: metadata.SourceEXIF, Key: metadata.KeyHeight, ValueType: metadata.ValueTypeInt, Value: "tall"}
	unknown := metadata.Entry{Source: metadata.SourceEXIF, Key: "maker_note", ValueType: metadata.ValueTypeText, Value: "x"}

	created := metadataFileChange(string(model.ReplicaFileActionCreated), nil, "photo.jpg", "hash-1", captured, width, invalid, unknown)
	if err := fixture.svc.ReportFileChanges(fixture.replica.ID, "node-a", []ReplicaFileChangeInput{created}); err != nil {
		t.Fatalf("ReportFileChanges(created) error = %v", err)
	}
	file, rows := fileMetadataRows(t, fixture, "photo.jpg")
	if len(rows) != 2 || rows[0].Key != metadata.KeyCaptureTime || rows[1].Key != metadata.KeyWidth || rows[0].FileVersion != 1 {
		t.Fatalf("metadata rows after create = %+v", rows)
	}

	files, err := fixture.svc.ListInventoryFiles(fixture.replica.ID, "node-a")
	if err != nil {
		t.Fatalf("ListInventoryFiles() error = %v", err)
	}
	var listed map[string]string
	for _, item := range files {
		if item.FileID == file.ID {
			listed = item.Metadata
		}
	}
	want := map[string]string{metadata.KeyCaptureTime: "2024-05-01T10:20:30", metadata.KeyWidth: "4000"}
	if !reflect.DeepEqual(listed, want) {
		t.Fatalf("listed metadata = %#v, want %#v", listed, want)
	}

	fileID := file.ID
	updated := metadataFileChange(string(model.ReplicaFileActionUpdated), &fileID, "photo.jpg", "hash-2", width)
	if err := fixture.svc.ReportFileChanges(fixture.replica.ID, "node-a", []ReplicaFileChangeInput{updated}); err != nil {
		t.Fatalf("ReportFileChanges(updated) error = %v", err)
	}
	_, rows = fileMetadataRows(t, fixture, "photo.jpg")
	if len(rows) != 1 || rows[0].Key != metadata.KeyWidth || rows[0].FileVersion != 2 {
		t.Fatalf("metadata rows after update = %+v", rows)
	}

	withoutMetadata := metadataFileChange(string(model.ReplicaFileActionUpdated), &fileID, "photo.jpg", "hash-3")
	if err := fixture.svc.ReportFileChanges(fixture.replica.ID, "node-a", []ReplicaFileChangeInput{withoutMetadata}); err != nil {
		t.Fatalf("ReportFileChanges(update without metadata) error = %v", err)
	}
	if _, rows = fileMetadataRows(t, fixture, "photo.jpg"); len(rows) != 0 {
		t.Fatalf("metadata rows after update without metadata = %+v", rows)
	}
}

func TestReportFileChangesFillsMissingMetadataForUnchangedContent(t *testing.T) {
	fixture := newShareFileReportFixture(t)
	file, rows := fileMetadataRows(t, fixture, "album/cover.jpg")
	if len(rows) != 0 {
		t.Fatalf("unexpected initial metadata %+v", rows)
	}
	fileID := file.ID
	duration := metadata.Entry{Source: metadata.SourceMP4, Key: metadata.KeyDurationMS, ValueType: metadata.ValueTypeInt, Value: "1500"}
	same := metadataFileChange(string(model.ReplicaFileActionUpdated), &fileID, file.RelativeURI, file.Hash, duration)
	same.FileSize = file.Size
	if err := fixture.svc.ReportFileChanges(fixture.replica.ID, "node-a", []ReplicaFileChangeInput{same}); err != nil {
		t.Fatalf("ReportFileChanges() error = %v", err)
	}
	after, rows := fileMetadataRows(t, fixture, "album/cover.jpg")
	if after.Version != file.Version {
		t.Fatalf("version = %d, want unchanged %d", after.Version, file.Version)
	}
	if len(rows) != 1 || rows[0].Value != "1500" || rows[0].FileVersion != file.Version {
		t.Fatalf("metadata rows = %+v", rows)
	}
}

func TestReportFileChangesIgnoresDownstreamMetadata(t *testing.T) {
	fixture := newShareFileReportFixture(t)
	file, _ := fileMetadataRows(t, fixture, "album/cover.jpg")
	mustCreate(t, fixture.database, &model.ReplicaFile{FileID: file.ID, ReplicaID: fixture.downstream.ID, Version: 1, Status: model.ReplicaFileStatusSynchronized})
	fileID := file.ID
	width := metadata.Entry{Source: metadata.SourceImage, Key: metadata.KeyWidth, ValueType: metadata.ValueTypeInt, Value: "10"}
	change := metadataFileChange(string(model.ReplicaFileActionUpdated), &fileID, file.RelativeURI, "local-edit", width)
	if err := fixture.svc.ReportFileChanges(fixture.downstream.ID, "node-a", []ReplicaFileChangeInput{change}); err != nil {
		t.Fatalf("ReportFileChanges() error = %v", err)
	}
	if _, rows := fileMetadataRows(t, fixture, "album/cover.jpg"); len(rows) != 0 {
		t.Fatalf("downstream report stored metadata %+v", rows)
	}
}

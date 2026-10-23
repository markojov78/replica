package repository

import (
	"time"

	"replica/internal/metadata"
	"replica/internal/model"

	"gorm.io/gorm"
)

// replaceFileMetadata replaces all metadata of a file with entries extracted from the given file version.
func replaceFileMetadata(tx *gorm.DB, fileID, version uint, entries []metadata.Entry) error {
	if err := tx.Where("file_id = ?", fileID).Delete(&model.FileMetadata{}).Error; err != nil {
		return err
	}
	return createFileMetadata(tx, fileID, version, entries)
}

// fillMissingFileMetadata stores entries for a file version without metadata. Entries are content-bound, so an
// unchanged file reported again with metadata can fill it without a new version.
func fillMissingFileMetadata(tx *gorm.DB, fileID, version uint, entries []metadata.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	var count int64
	if err := tx.Model(&model.FileMetadata{}).Where("file_id = ?", fileID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return createFileMetadata(tx, fileID, version, entries)
}

func createFileMetadata(tx *gorm.DB, fileID, version uint, entries []metadata.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	now := time.Now().UTC()
	rows := make([]model.FileMetadata, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, model.FileMetadata{
			FileID:        fileID,
			FileVersion:   version,
			ExtractedTime: now,
			Source:        entry.Source,
			Key:           entry.Key,
			ValueType:     entry.ValueType,
			Value:         entry.Value,
		})
	}
	return tx.Create(&rows).Error
}

// fileMetadataValues returns the metadata of files selected by query, by file ID and key.
func fileMetadataValues(query *gorm.DB) (map[uint]map[string]string, error) {
	var rows []model.FileMetadata
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[uint]map[string]string)
	for _, row := range rows {
		values := result[row.FileID]
		if values == nil {
			values = make(map[string]string)
			result[row.FileID] = values
		}
		values[row.Key] = row.Value
	}
	return result, nil
}

// FindFileMetadataValues returns the metadata of one inventory file by key.
func (r *InventoryRepository) FindFileMetadataValues(fileID uint) (map[string]string, error) {
	values, err := fileMetadataValues(r.db.Where("file_id = ?", fileID))
	if err != nil {
		return nil, err
	}
	return values[fileID], nil
}

// replicaFileMetadataValues returns the metadata of all files of a replica, optionally limited to a replica file
// status, by file ID and key.
func replicaFileMetadataValues(db *gorm.DB, replicaID uint, status string) (map[uint]map[string]string, error) {
	query := db.
		Model(&model.FileMetadata{}).
		Select("file_metadata.*").
		Joins("JOIN replica_files ON replica_files.file_id = file_metadata.file_id").
		Where("replica_files.replica_id = ?", replicaID)
	if status != "" {
		query = query.Where("replica_files.status = ?", status)
	}
	return fileMetadataValues(query)
}

package repository

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"dropoutbox/internal/model"

	"gorm.io/gorm"
)

type ReplicaRepository struct {
	db *gorm.DB
}

func NewReplicaRepository(db *gorm.DB) *ReplicaRepository {
	return &ReplicaRepository{db: db}
}

type ReplicaFileUpdate struct {
	FileID       *uint
	RelativeURI  string
	FileSize     int64
	FileHash     string
	CreatedTime  time.Time
	ModifiedTime time.Time
}

var ErrInvalidReplicaFileUpdate = errors.New("invalid replica file update")

type ReplicaInventoryFile struct {
	FileID           uint
	ReplicaID        uint
	InventoryID      uint
	RelativeURI      string
	Size             int64
	Hash             string
	InventoryStatus  string
	InventoryVersion uint
	ReplicaStatus    string
	ReplicaVersion   uint
	Created          time.Time
	Modified         time.Time
}

func (r *ReplicaRepository) List() ([]model.Replica, error) {
	var replicas []model.Replica
	err := r.db.Order("id asc").Find(&replicas).Error
	return replicas, err
}

func (r *ReplicaRepository) Create(replica *model.Replica) error {
	return r.db.Create(replica).Error
}

func (r *ReplicaRepository) CreateWithPendingFiles(replica *model.Replica, command *model.Command) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(replica).Error; err != nil {
			return err
		}

		var files []model.InventoryFile
		if err := tx.Where("inventory_id = ?", replica.InventoryID).Order("id asc").Find(&files).Error; err != nil {
			return err
		}

		replicaFiles := make([]model.ReplicaFile, 0, len(files))
		for _, file := range files {
			replicaFiles = append(replicaFiles, model.ReplicaFile{
				FileID:    file.ID,
				ReplicaID: replica.ID,
				Version:   0,
				Status:    model.ReplicaFileStatusPending,
			})
		}
		if len(replicaFiles) > 0 {
			if err := tx.Create(&replicaFiles).Error; err != nil {
				return err
			}
		}

		if command == nil {
			return nil
		}
		command.NodeID = replica.NodeID
		if command.Type == model.NodeCommandTypeReconcileReplica {
			payload, err := json.Marshal(map[string]uint{
				"replica_id": replica.ID,
			})
			if err != nil {
				return err
			}
			command.Payload = payload
		} else if len(command.Payload) == 0 {
			command.Payload = []byte("{}")
		}
		return tx.Create(command).Error
	})
}

func (r *ReplicaRepository) FindByID(id uint) (*model.Replica, error) {
	var replica model.Replica
	if err := r.db.First(&replica, id).Error; err != nil {
		return nil, err
	}
	return &replica, nil
}

func (r *ReplicaRepository) FindFileByID(replicaID, fileID uint) (*model.ReplicaFile, error) {
	var file model.ReplicaFile
	if err := r.db.Where("replica_id = ? AND file_id = ?", replicaID, fileID).First(&file).Error; err != nil {
		return nil, err
	}
	return &file, nil
}

type ReplicaListFilter struct {
	InventoryID *uint
	NodeID      string
	URIPrefix   string
}

type ReplicaFileListFilter struct {
	Status  string
	Version *uint
}

func (r *ReplicaRepository) ListFiltered(filter ReplicaListFilter) ([]model.Replica, error) {
	var replicas []model.Replica
	query := r.db.Order("id asc")
	if filter.InventoryID != nil {
		query = query.Where("inventory_id = ?", *filter.InventoryID)
	}
	if strings.TrimSpace(filter.NodeID) != "" {
		query = query.Where("node_id = ?", strings.TrimSpace(filter.NodeID))
	}
	if strings.TrimSpace(filter.URIPrefix) != "" {
		query = query.Where("uri LIKE ?", strings.TrimSpace(filter.URIPrefix)+"%")
	}

	err := query.Find(&replicas).Error
	return replicas, err
}

func (r *ReplicaRepository) ListPage(page, perPage int, filter ReplicaListFilter) ([]model.Replica, int64, error) {
	query := r.db.Model(&model.Replica{})
	if filter.InventoryID != nil {
		query = query.Where("inventory_id = ?", *filter.InventoryID)
	}
	if strings.TrimSpace(filter.NodeID) != "" {
		query = query.Where("node_id = ?", strings.TrimSpace(filter.NodeID))
	}
	if strings.TrimSpace(filter.URIPrefix) != "" {
		query = query.Where("uri LIKE ?", strings.TrimSpace(filter.URIPrefix)+"%")
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var replicas []model.Replica
	err := r.db.
		Scopes(func(tx *gorm.DB) *gorm.DB {
			if filter.InventoryID != nil {
				tx = tx.Where("inventory_id = ?", *filter.InventoryID)
			}
			if strings.TrimSpace(filter.NodeID) != "" {
				tx = tx.Where("node_id = ?", strings.TrimSpace(filter.NodeID))
			}
			if strings.TrimSpace(filter.URIPrefix) != "" {
				tx = tx.Where("uri LIKE ?", strings.TrimSpace(filter.URIPrefix)+"%")
			}
			return tx
		}).
		Order("id asc").
		Limit(perPage).
		Offset((page - 1) * perPage).
		Find(&replicas).Error
	if err != nil {
		return nil, 0, err
	}

	return replicas, total, nil
}

func (r *ReplicaRepository) ListFiles(replicaID uint, page, perPage int, filter ReplicaFileListFilter) ([]model.ReplicaFile, int64, error) {
	query := r.db.Model(&model.ReplicaFile{}).Where("replica_id = ?", replicaID)
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	if filter.Version != nil {
		query = query.Where("version = ?", *filter.Version)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var files []model.ReplicaFile
	err := r.db.
		Where("replica_id = ?", replicaID).
		Scopes(func(tx *gorm.DB) *gorm.DB {
			if filter.Status != "" {
				tx = tx.Where("status = ?", filter.Status)
			}
			if filter.Version != nil {
				tx = tx.Where("version = ?", *filter.Version)
			}
			return tx
		}).
		Order("file_id asc").
		Limit(perPage).
		Offset((page - 1) * perPage).
		Find(&files).Error
	if err != nil {
		return nil, 0, err
	}

	return files, total, nil
}

func (r *ReplicaRepository) ListInventoryFiles(replicaID uint) ([]ReplicaInventoryFile, error) {
	var files []ReplicaInventoryFile
	err := r.db.
		Table("replica_files").
		Select(`
			replica_files.file_id AS file_id,
			replica_files.replica_id AS replica_id,
			inventory_files.inventory_id AS inventory_id,
			inventory_files.relative_uri AS relative_uri,
			inventory_files.size AS size,
			inventory_files.hash AS hash,
			inventory_files.status AS inventory_status,
			inventory_files.version AS inventory_version,
			replica_files.status AS replica_status,
			replica_files.version AS replica_version,
			inventory_files.created AS created,
			inventory_files.modified AS modified
		`).
		Joins("JOIN inventory_files ON inventory_files.id = replica_files.file_id").
		Where("replica_files.replica_id = ?", replicaID).
		Order("inventory_files.relative_uri asc").
		Scan(&files).Error
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (r *ReplicaRepository) Update(replica *model.Replica) error {
	return r.db.Save(replica).Error
}

func (r *ReplicaRepository) ReportFileChanges(replicaID uint, updates []ReplicaFileUpdate) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var replica model.Replica
		if err := tx.First(&replica, replicaID).Error; err != nil {
			return err
		}

		for _, update := range updates {
			file, created, restored, err := r.resolveReportedInventoryFile(tx, replica.InventoryID, update)
			if err != nil {
				return err
			}

			oldVersion := file.Version
			if created {
				file.Version = 1
			} else {
				file.Version++
			}
			file.Status = model.InventoryFileStatusActive
			file.Created = update.CreatedTime
			file.Modified = update.ModifiedTime
			file.Size = update.FileSize
			file.Hash = update.FileHash
			if err := tx.Save(file).Error; err != nil {
				return err
			}

			action := model.FileJournalActionUpdated
			if created {
				action = model.FileJournalActionCreated
			} else if restored {
				action = model.FileJournalActionRestored
			}
			journal := model.FileJournal{
				FileID:      file.ID,
				InventoryID: file.InventoryID,
				ReplicaID:   replica.ID,
				Version:     oldVersion,
				Action:      action,
				Timestamp:   update.ModifiedTime,
			}
			if err := tx.Create(&journal).Error; err != nil {
				return err
			}

			var sourceReplicaFile model.ReplicaFile
			err = tx.Where("file_id = ? AND replica_id = ?", file.ID, replica.ID).First(&sourceReplicaFile).Error
			switch {
			case err == nil:
				sourceReplicaFile.Version = file.Version
				sourceReplicaFile.Status = model.ReplicaFileStatusSynchronized
				if err := tx.Save(&sourceReplicaFile).Error; err != nil {
					return err
				}
			case errors.Is(err, gorm.ErrRecordNotFound):
				sourceReplicaFile = model.ReplicaFile{
					FileID:    file.ID,
					ReplicaID: replica.ID,
					Version:   file.Version,
					Status:    model.ReplicaFileStatusSynchronized,
				}
				if err := tx.Create(&sourceReplicaFile).Error; err != nil {
					return err
				}
			default:
				return err
			}

			if err := tx.Model(&model.ReplicaFile{}).
				Where("file_id = ? AND replica_id <> ?", file.ID, replica.ID).
				Update("status", model.ReplicaFileStatusPending).Error; err != nil {
				return err
			}
		}

		return nil
	})
}

func (r *ReplicaRepository) resolveReportedInventoryFile(tx *gorm.DB, inventoryID uint, update ReplicaFileUpdate) (*model.InventoryFile, bool, bool, error) {
	if update.FileID != nil {
		var file model.InventoryFile
		if err := tx.First(&file, *update.FileID).Error; err != nil {
			return nil, false, false, err
		}
		if file.InventoryID != inventoryID || file.RelativeURI != update.RelativeURI {
			return nil, false, false, ErrInvalidReplicaFileUpdate
		}
		return &file, false, file.Status == model.InventoryFileStatusDeleted, nil
	}

	var existing model.InventoryFile
	err := tx.
		Where("inventory_id = ? AND relative_uri = ?", inventoryID, update.RelativeURI).
		First(&existing).Error
	switch {
	case err == nil:
		if existing.Status == model.InventoryFileStatusDeleted {
			return &existing, false, true, nil
		}
		return nil, false, false, ErrInvalidReplicaFileUpdate
	case errors.Is(err, gorm.ErrRecordNotFound):
		return &model.InventoryFile{
			InventoryID: inventoryID,
			RelativeURI: update.RelativeURI,
			Status:      model.InventoryFileStatusActive,
		}, true, false, nil
	default:
		return nil, false, false, err
	}
}

package repository

import (
	"dropoutbox/internal/model"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

type InventoryRepository struct {
	db *gorm.DB
}

type ReplicaFileUpdate struct {
	FileID       uint
	FileSize     int64
	FileHash     string
	ModifiedTime time.Time
}

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

func NewInventoryRepository(db *gorm.DB) *InventoryRepository {
	return &InventoryRepository{db: db}
}

func (r *InventoryRepository) CreateWithDefaultReplica(inventory *model.Inventory, replica *model.Replica, command *model.Command, creatorUserID uint, permissions []string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(inventory).Error; err != nil {
			return err
		}

		replica.InventoryID = inventory.ID
		if err := tx.Create(replica).Error; err != nil {
			return err
		}

		inventoryUser := &model.InventoryUser{
			UserID:      creatorUserID,
			InventoryID: inventory.ID,
		}
		if err := tx.Create(inventoryUser).Error; err != nil {
			return err
		}

		if len(permissions) == 0 {
			return nil
		}

		rows := make([]model.InventoryPermission, 0, len(permissions))
		for _, permission := range permissions {
			rows = append(rows, model.InventoryPermission{
				InventoryUserID: inventoryUser.ID,
				Permission:      permission,
			})
		}

		if err := tx.Create(&rows).Error; err != nil {
			return err
		}

		if command == nil {
			return nil
		}

		command.NodeID = replica.NodeID
		if command.Type == model.NodeCommandTypeScanReplica {
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

func (r *InventoryRepository) FindByID(id uint) (*model.Inventory, error) {
	var inventory model.Inventory
	if err := r.preloadDetails(r.db).First(&inventory, id).Error; err != nil {
		return nil, err
	}
	return &inventory, nil
}

func (r *InventoryRepository) List(page, perPage int) ([]model.Inventory, int64, error) {
	var total int64
	if err := r.db.Model(&model.Inventory{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var inventories []model.Inventory
	err := r.preloadDetails(r.db).
		Order("inventories.id asc").
		Limit(perPage).
		Offset((page - 1) * perPage).
		Find(&inventories).Error
	if err != nil {
		return nil, 0, err
	}

	return inventories, total, nil
}

func (r *InventoryRepository) Update(inventory *model.Inventory) error {
	return r.db.Save(inventory).Error
}

func (r *InventoryRepository) FindFileByID(inventoryID, fileID uint) (*model.InventoryFile, error) {
	var file model.InventoryFile
	if err := r.db.Where("inventory_id = ? AND id = ?", inventoryID, fileID).First(&file).Error; err != nil {
		return nil, err
	}
	return &file, nil
}

func (r *InventoryRepository) CreateReplica(replica *model.Replica) error {
	return r.db.Create(replica).Error
}

func (r *InventoryRepository) FindReplicaByID(id uint) (*model.Replica, error) {
	var replica model.Replica
	if err := r.db.First(&replica, id).Error; err != nil {
		return nil, err
	}
	return &replica, nil
}

func (r *InventoryRepository) FindReplicaFileByID(replicaID, fileID uint) (*model.ReplicaFile, error) {
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

func (r *InventoryRepository) ListReplicas(filter ReplicaListFilter) ([]model.Replica, error) {
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

func (r *InventoryRepository) ListReplicasPage(page, perPage int, filter ReplicaListFilter) ([]model.Replica, int64, error) {
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

func (r *InventoryRepository) ListFiles(inventoryID uint, page, perPage int) ([]model.InventoryFile, int64, error) {
	var total int64
	if err := r.db.Model(&model.InventoryFile{}).Where("inventory_id = ?", inventoryID).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var files []model.InventoryFile
	err := r.db.
		Where("inventory_id = ?", inventoryID).
		Order("id asc").
		Limit(perPage).
		Offset((page - 1) * perPage).
		Find(&files).Error
	if err != nil {
		return nil, 0, err
	}

	return files, total, nil
}

func (r *InventoryRepository) ListReplicaFiles(replicaID uint, page, perPage int, filter ReplicaFileListFilter) ([]model.ReplicaFile, int64, error) {
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

func (r *InventoryRepository) ListReplicaInventoryFiles(replicaID uint) ([]ReplicaInventoryFile, error) {
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

func (r *InventoryRepository) UpdateReplica(replica *model.Replica) error {
	return r.db.Save(replica).Error
}

func (r *InventoryRepository) ReportReplicaFileChanges(replicaID uint, updates []ReplicaFileUpdate) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var replica model.Replica
		if err := tx.First(&replica, replicaID).Error; err != nil {
			return err
		}

		for _, update := range updates {
			var file model.InventoryFile
			if err := tx.Where("inventory_id = ? AND id = ?", replica.InventoryID, update.FileID).First(&file).Error; err != nil {
				return err
			}

			oldVersion := file.Version
			file.Version++
			file.Status = model.InventoryFileStatusActive
			file.Modified = update.ModifiedTime
			file.Size = update.FileSize
			file.Hash = update.FileHash
			if err := tx.Save(&file).Error; err != nil {
				return err
			}

			journal := model.FileJournal{
				FileID:      file.ID,
				InventoryID: file.InventoryID,
				ReplicaID:   replica.ID,
				Version:     oldVersion,
				Action:      model.FileJournalActionUpdated,
				Timestamp:   update.ModifiedTime,
			}
			if err := tx.Create(&journal).Error; err != nil {
				return err
			}

			var sourceReplicaFile model.ReplicaFile
			err := tx.Where("file_id = ? AND replica_id = ?", file.ID, replica.ID).First(&sourceReplicaFile).Error
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

func (r *InventoryRepository) preloadDetails(db *gorm.DB) *gorm.DB {
	return db.Preload("Replicas", func(tx *gorm.DB) *gorm.DB {
		return tx.Order("replicas.id asc")
	})
}

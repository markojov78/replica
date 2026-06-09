package repository

import (
	"encoding/json"

	"replica/internal/model"

	"gorm.io/gorm"
)

type InventoryRepository struct {
	db *gorm.DB
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

func (r *InventoryRepository) preloadDetails(db *gorm.DB) *gorm.DB {
	return db.Preload("Replicas", func(tx *gorm.DB) *gorm.DB {
		return tx.Order("replicas.id asc")
	})
}

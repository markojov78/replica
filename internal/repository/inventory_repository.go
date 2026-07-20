package repository

import (
	"encoding/json"
	"strings"

	"replica/internal/model"

	"gorm.io/gorm"
)

type InventoryRepository struct {
	db *gorm.DB
}

type UserPermissionDetails struct {
	UserID      uint
	Permissions []string
}

func NewInventoryRepository(db *gorm.DB) *InventoryRepository {
	return &InventoryRepository{db: db}
}

func (r *InventoryRepository) CreateWithDefaultReplica(inventory *model.Inventory, replica *model.Replica, inventoryFiles []model.InventoryFile, command, refreshCommand *model.Command, permissions []UserPermissionDetails) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(inventory).Error; err != nil {
			return err
		}

		replica.InventoryID = inventory.ID
		if err := tx.Create(replica).Error; err != nil {
			return err
		}

		for i := range inventoryFiles {
			inventoryFiles[i].InventoryID = inventory.ID
			if err := tx.Create(&inventoryFiles[i]).Error; err != nil {
				return err
			}
			if err := tx.Create(&model.ReplicaFile{
				FileID:    inventoryFiles[i].ID,
				ReplicaID: replica.ID,
				Version:   0,
				Status:    model.ReplicaFileStatusSynchronized,
			}).Error; err != nil {
				return err
			}
		}

		if err := replaceInventoryUserPermissions(tx, inventory.ID, permissions); err != nil {
			return err
		}

		if command != nil {
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
			if err := tx.Create(command).Error; err != nil {
				return err
			}
		}

		if refreshCommand == nil {
			return nil
		}
		refreshCommand.NodeID = replica.NodeID
		if len(refreshCommand.Payload) == 0 {
			refreshCommand.Payload = []byte("{}")
		}
		return tx.Create(refreshCommand).Error
	})
}

func (r *InventoryRepository) FindByID(id uint) (*model.Inventory, error) {
	var inventory model.Inventory
	if err := r.preloadDetails(r.db).First(&inventory, id).Error; err != nil {
		return nil, err
	}
	return &inventory, nil
}

type InventoryListFilter struct {
	Status string
}

type InventoryFileListFilter struct {
	Status string
	Sort   string
	Order  string
}

func (r *InventoryRepository) List(page, perPage int, filter InventoryListFilter) ([]model.Inventory, int64, error) {
	query := r.db.Model(&model.Inventory{})
	if strings.TrimSpace(filter.Status) != "" {
		query = query.Where("status = ?", strings.TrimSpace(filter.Status))
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var inventories []model.Inventory
	err := r.preloadDetails(query).
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

func (r *InventoryRepository) UpdateWithUserPermissions(inventory *model.Inventory, permissions []UserPermissionDetails, replacePermissions bool) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(inventory).Error; err != nil {
			return err
		}
		if !replacePermissions {
			return nil
		}
		return replaceInventoryUserPermissions(tx, inventory.ID, permissions)
	})
}

func (r *InventoryRepository) UserPermissions(inventoryID uint) ([]UserPermissionDetails, error) {
	var users []model.InventoryUser
	if err := r.db.Where("inventory_id = ?", inventoryID).Order("user_id asc").Find(&users).Error; err != nil {
		return nil, err
	}

	result := make([]UserPermissionDetails, 0, len(users))
	for _, user := range users {
		var permissions []model.InventoryPermission
		if err := r.db.Where("inventory_user_id = ?", user.ID).Order("permission asc").Find(&permissions).Error; err != nil {
			return nil, err
		}
		if len(permissions) == 0 {
			continue
		}
		detail := UserPermissionDetails{
			UserID:      user.UserID,
			Permissions: make([]string, 0, len(permissions)),
		}
		for _, permission := range permissions {
			detail.Permissions = append(detail.Permissions, permission.Permission)
		}
		result = append(result, detail)
	}
	return result, nil
}

func (r *InventoryRepository) FindFileByID(inventoryID, fileID uint) (*model.InventoryFile, error) {
	var file model.InventoryFile
	if err := r.db.Where("inventory_id = ? AND id = ?", inventoryID, fileID).First(&file).Error; err != nil {
		return nil, err
	}
	return &file, nil
}

func (r *InventoryRepository) ListFiles(inventoryID uint, page, perPage int, filter InventoryFileListFilter) ([]model.InventoryFile, int64, error) {
	query := r.db.Model(&model.InventoryFile{}).Where("inventory_id = ?", inventoryID)
	if strings.TrimSpace(filter.Status) != "" {
		query = query.Where("status = ?", strings.TrimSpace(filter.Status))
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var files []model.InventoryFile
	sortColumn := filter.Sort
	if sortColumn == "name" {
		sortColumn = "relative_uri"
	}
	err := query.
		Order(sortColumn + " " + filter.Order).
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

func replaceInventoryUserPermissions(tx *gorm.DB, inventoryID uint, permissions []UserPermissionDetails) error {
	var users []model.InventoryUser
	if err := tx.Where("inventory_id = ?", inventoryID).Find(&users).Error; err != nil {
		return err
	}
	userIDs := make([]uint, 0, len(users))
	for _, user := range users {
		userIDs = append(userIDs, user.ID)
	}
	if len(userIDs) > 0 {
		if err := tx.Where("inventory_user_id IN ?", userIDs).Delete(&model.InventoryPermission{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id IN ?", userIDs).Delete(&model.InventoryUser{}).Error; err != nil {
			return err
		}
	}

	for _, permission := range permissions {
		inventoryUser := &model.InventoryUser{
			UserID:      permission.UserID,
			InventoryID: inventoryID,
		}
		if err := tx.Create(inventoryUser).Error; err != nil {
			return err
		}
		rows := make([]model.InventoryPermission, 0, len(permission.Permissions))
		for _, action := range permission.Permissions {
			rows = append(rows, model.InventoryPermission{
				InventoryUserID: inventoryUser.ID,
				Permission:      action,
			})
		}
		if len(rows) > 0 {
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

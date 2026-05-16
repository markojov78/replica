package repository

import (
	"dropoutbox/internal/model"

	"gorm.io/gorm"
)

type InventoryRepository struct {
	db *gorm.DB
}

func NewInventoryRepository(db *gorm.DB) *InventoryRepository {
	return &InventoryRepository{db: db}
}

func (r *InventoryRepository) List() ([]model.Inventory, error) {
	var inventories []model.Inventory
	err := r.db.Order("id asc").Find(&inventories).Error
	return inventories, err
}

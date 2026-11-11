package repository

import "gorm.io/gorm"

// ChangeLogRepository stores inventory, replica and node change log rows.
type ChangeLogRepository struct {
	db *gorm.DB
}

func NewChangeLogRepository(db *gorm.DB) *ChangeLogRepository {
	return &ChangeLogRepository{db: db}
}

func (r *ChangeLogRepository) WithTx(tx *gorm.DB) *ChangeLogRepository {
	return &ChangeLogRepository{db: tx}
}

// Create inserts one change log row (model.InventoryLog, model.ReplicaLog or model.NodeLog).
func (r *ChangeLogRepository) Create(row any) error {
	return r.db.Create(row).Error
}

// Transaction runs fn in a database transaction.
func (r *ChangeLogRepository) Transaction(fn func(tx *gorm.DB) error) error {
	return r.db.Transaction(fn)
}

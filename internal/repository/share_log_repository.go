package repository

import (
	"replica/internal/model"

	"gorm.io/gorm"
)

type ShareLogRepository struct {
	db *gorm.DB
}

func NewShareLogRepository(db *gorm.DB) *ShareLogRepository {
	return &ShareLogRepository{db: db}
}

func (r *ShareLogRepository) WithTx(tx *gorm.DB) *ShareLogRepository {
	return &ShareLogRepository{db: tx}
}

func (r *ShareLogRepository) Create(entry *model.ShareLog) error {
	return r.db.Create(entry).Error
}

// Transaction runs fn in a database transaction.
func (r *ShareLogRepository) Transaction(fn func(tx *gorm.DB) error) error {
	return r.db.Transaction(fn)
}

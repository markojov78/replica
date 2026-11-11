package repository

import (
	"replica/internal/model"

	"gorm.io/gorm"
)

type UserLogRepository struct {
	db *gorm.DB
}

func NewUserLogRepository(db *gorm.DB) *UserLogRepository {
	return &UserLogRepository{db: db}
}

func (r *UserLogRepository) WithTx(tx *gorm.DB) *UserLogRepository {
	return &UserLogRepository{db: tx}
}

func (r *UserLogRepository) Create(entry *model.UserLog) error {
	return r.db.Create(entry).Error
}

// Transaction runs fn in a database transaction.
func (r *UserLogRepository) Transaction(fn func(tx *gorm.DB) error) error {
	return r.db.Transaction(fn)
}

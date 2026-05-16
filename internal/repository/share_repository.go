package repository

import (
	"dropoutbox/internal/model"

	"gorm.io/gorm"
)

type ShareRepository struct {
	db *gorm.DB
}

func NewShareRepository(db *gorm.DB) *ShareRepository {
	return &ShareRepository{db: db}
}

func (r *ShareRepository) List() ([]model.Share, error) {
	var shares []model.Share
	err := r.db.Order("id asc").Find(&shares).Error
	return shares, err
}

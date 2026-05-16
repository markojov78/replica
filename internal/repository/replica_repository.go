package repository

import (
	"dropoutbox/internal/model"

	"gorm.io/gorm"
)

type ReplicaRepository struct {
	db *gorm.DB
}

func NewReplicaRepository(db *gorm.DB) *ReplicaRepository {
	return &ReplicaRepository{db: db}
}

func (r *ReplicaRepository) List() ([]model.Replica, error) {
	var replicas []model.Replica
	err := r.db.Order("id asc").Find(&replicas).Error
	return replicas, err
}

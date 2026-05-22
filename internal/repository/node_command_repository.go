package repository

import (
	"dropoutbox/internal/model"
	"time"

	"gorm.io/gorm"
)

type NodeCommandRepository struct {
	db *gorm.DB
}

func NewNodeCommandRepository(db *gorm.DB) *NodeCommandRepository {
	return &NodeCommandRepository{db: db}
}

func (r *NodeCommandRepository) Create(command *model.NodeCommand) error {
	now := time.Now().UTC()
	if command.CreatedAt.IsZero() {
		command.CreatedAt = now
	}
	command.UpdatedAt = command.CreatedAt
	return r.db.Create(command).Error
}

func (r *NodeCommandRepository) FindByID(id uint) (*model.NodeCommand, error) {
	var command model.NodeCommand
	if err := r.db.First(&command, id).Error; err != nil {
		return nil, err
	}
	return &command, nil
}

func (r *NodeCommandRepository) ListPendingByNodeID(nodeID string) ([]model.NodeCommand, error) {
	var commands []model.NodeCommand
	err := r.db.
		Where("node_id = ? AND status = ?", nodeID, model.NodeCommandStatusPending).
		Order("id asc").
		Find(&commands).Error
	if err != nil {
		return nil, err
	}
	return commands, nil
}

func (r *NodeCommandRepository) Update(command *model.NodeCommand) error {
	command.UpdatedAt = time.Now().UTC()
	return r.db.Save(command).Error
}

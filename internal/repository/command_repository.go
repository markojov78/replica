package repository

import (
	"replica/internal/model"
	"time"

	"gorm.io/gorm"
)

type CommandRepository struct {
	db *gorm.DB
}

func NewNodeCommandRepository(db *gorm.DB) *CommandRepository {
	return &CommandRepository{db: db}
}

func (r *CommandRepository) Create(command *model.Command) error {
	now := time.Now().UTC()
	if command.CreatedAt.IsZero() {
		command.CreatedAt = now
	}
	command.UpdatedAt = command.CreatedAt
	return r.db.Create(command).Error
}

func (r *CommandRepository) FindByID(id uint) (*model.Command, error) {
	var command model.Command
	if err := r.db.First(&command, id).Error; err != nil {
		return nil, err
	}
	return &command, nil
}

func (r *CommandRepository) ListPendingByNodeID(nodeID string) ([]model.Command, error) {
	var commands []model.Command
	err := r.db.
		Where("node_id = ? AND status = ?", nodeID, model.NodeCommandStatusPending).
		Order("id asc").
		Find(&commands).Error
	if err != nil {
		return nil, err
	}
	return commands, nil
}

func (r *CommandRepository) Update(command *model.Command) error {
	command.UpdatedAt = time.Now().UTC()
	return r.db.Save(command).Error
}

package repository

import (
	"replica/internal/model"
	"time"

	"gorm.io/gorm"
)

type CommandListFilter struct {
	NodeID        string
	Type          model.CommandType
	Status        model.CommandStatus
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	Sort          string
	Order         string
}

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

func (r *CommandRepository) List(page, perPage int, filter CommandListFilter) ([]model.Command, int64, error) {
	query := r.db.Model(&model.Command{})
	if filter.NodeID != "" {
		query = query.Where("node_id = ?", filter.NodeID)
	}
	if filter.Type != "" {
		query = query.Where("type = ?", filter.Type)
	}
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	if filter.CreatedAfter != nil {
		query = query.Where("created_at > ?", *filter.CreatedAfter)
	}
	if filter.CreatedBefore != nil {
		query = query.Where("created_at < ?", *filter.CreatedBefore)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var commands []model.Command
	err := query.Order(filter.Sort + " " + filter.Order).Offset((page - 1) * perPage).Limit(perPage).Find(&commands).Error
	return commands, total, err
}

func (r *CommandRepository) Update(command *model.Command) error {
	command.UpdatedAt = time.Now().UTC()
	return r.db.Save(command).Error
}

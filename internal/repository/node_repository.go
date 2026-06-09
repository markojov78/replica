package repository

import (
	"replica/internal/model"

	"gorm.io/gorm"
)

func NewNodeRepository(db *gorm.DB) *NodeRepository {
	return &NodeRepository{db: db}
}

type NodeRepository struct {
	db *gorm.DB
}

func (r *NodeRepository) Create(node *model.Node) error {
	return r.db.Create(node).Error
}

func (r *NodeRepository) FindByID(id string) (*model.Node, error) {
	var node model.Node
	if err := r.db.First(&node, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &node, nil
}

func (r *NodeRepository) List(page, perPage int) ([]model.Node, int64, error) {
	var total int64
	if err := r.db.Model(&model.Node{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var nodes []model.Node
	err := r.db.
		Order("nodes.id asc").
		Limit(perPage).
		Offset((page - 1) * perPage).
		Find(&nodes).Error
	if err != nil {
		return nil, 0, err
	}

	return nodes, total, nil
}

func (r *NodeRepository) ListAll() ([]model.Node, error) {
	var nodes []model.Node
	if err := r.db.Order("nodes.id asc").Find(&nodes).Error; err != nil {
		return nil, err
	}
	return nodes, nil
}

func (r *NodeRepository) Update(node *model.Node) error {
	return r.db.Save(node).Error
}

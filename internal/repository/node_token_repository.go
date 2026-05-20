package repository

import (
	"dropoutbox/internal/model"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func NewNodeTokenRepository(db *gorm.DB) *NodeTokenRepository {
	return &NodeTokenRepository{db: db}
}

type NodeTokenRepository struct {
	db *gorm.DB
}

func (r *NodeTokenRepository) Save(nodeToken *model.NodeToken) error {
	now := time.Now().UTC()
	nodeToken.UpdatedAt = now
	if nodeToken.CreatedAt.IsZero() {
		nodeToken.CreatedAt = now
	}

	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "node_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"refresh_hash":       nodeToken.RefreshHash,
			"refresh_expiration": nodeToken.RefreshExpiration,
			"revoked_at":         nodeToken.RevokedAt,
			"updated_at":         nodeToken.UpdatedAt,
		}),
	}).Create(nodeToken).Error
}

func (r *NodeTokenRepository) FindByRefreshHash(refreshHash string) (*model.NodeToken, error) {
	var nodeToken model.NodeToken
	err := r.db.
		Preload("Node").
		Where("refresh_hash = ?", refreshHash).
		First(&nodeToken).Error
	if err != nil {
		return nil, err
	}
	return &nodeToken, nil
}

func (r *NodeTokenRepository) DeleteByNodeID(nodeID string) error {
	return r.db.Delete(&model.NodeToken{}, "node_id = ?", nodeID).Error
}

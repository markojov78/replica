package repository

import (
	"encoding/json"
	"time"

	"replica/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ConfigRepository struct {
	db *gorm.DB
}

func NewConfigRepository(db *gorm.DB) *ConfigRepository {
	return &ConfigRepository{db: db}
}

func (r *ConfigRepository) FindSettings(keys ...string) (map[string]model.Setting, error) {
	settings := make([]model.Setting, 0, len(keys))
	if err := r.db.Where("key IN ?", keys).Find(&settings).Error; err != nil {
		return nil, err
	}

	result := make(map[string]model.Setting, len(settings))
	for _, setting := range settings {
		result[setting.Key] = setting
	}
	return result, nil
}

func (r *ConfigRepository) UpdateSettings(values map[string]string, commandStatuses []model.NodeStatus) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		for key, value := range values {
			setting := model.Setting{Key: key, Value: value}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "key"}},
				DoUpdates: clause.AssignmentColumns([]string{"value"}),
			}).Create(&setting).Error; err != nil {
				return err
			}
		}
		return createRefreshConfigCommands(tx, commandStatuses)
	})
}

func (r *ConfigRepository) DeleteSettings(keys []string, commandStatuses []model.NodeStatus) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("key IN ?", keys).Delete(&model.Setting{}).Error; err != nil {
			return err
		}
		return createRefreshConfigCommands(tx, commandStatuses)
	})
}

func createRefreshConfigCommands(tx *gorm.DB, nodeStatuses []model.NodeStatus) error {
	var nodes []model.Node
	if err := tx.Where("status IN ?", nodeStatuses).Order("id asc").Find(&nodes).Error; err != nil {
		return err
	}

	now := time.Now().UTC()
	payload := json.RawMessage(`{}`)
	for _, node := range nodes {
		command := model.Command{
			NodeID:    node.ID,
			Type:      model.NodeCommandTypeRefreshConfig,
			Status:    model.NodeCommandStatusPending,
			Payload:   payload,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := tx.Create(&command).Error; err != nil {
			return err
		}
	}
	return nil
}

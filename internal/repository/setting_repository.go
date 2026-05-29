package repository

import (
	"errors"

	"dropoutbox/internal/model"

	"gorm.io/gorm"
)

type SettingRepository struct {
	db *gorm.DB
}

func NewSettingRepository(db *gorm.DB) *SettingRepository {
	return &SettingRepository{db: db}
}

func (r *SettingRepository) FindByKey(key string) (*model.Setting, error) {
	var setting model.Setting
	if err := r.db.First(&setting, "key = ?", key).Error; err != nil {
		return nil, err
	}
	return &setting, nil
}

func (r *SettingRepository) FindExisting(keys ...string) (map[string]model.Setting, error) {
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

func (r *SettingRepository) Create(setting *model.Setting) error {
	return r.db.Create(setting).Error
}

func (r *SettingRepository) CreateMany(settings []model.Setting) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		for i := range settings {
			if err := tx.Create(&settings[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *SettingRepository) IsNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

package repository

import (
	"replica/internal/model"

	"gorm.io/gorm"
)

func NewUserTokenRepository(db *gorm.DB) *UserTokenRepository {
	return &UserTokenRepository{db: db}
}

type UserTokenRepository struct {
	db *gorm.DB
}

func (r *UserTokenRepository) Create(userToken *model.UserToken) error {
	return r.db.Create(userToken).Error
}

func (r *UserTokenRepository) FindByRefreshHash(refreshHash string) (*model.UserToken, error) {
	var userToken model.UserToken
	err := r.db.
		Where("refresh_hash = ?", refreshHash).
		First(&userToken).Error
	if err != nil {
		return nil, err
	}
	return &userToken, nil
}

func (r *UserTokenRepository) DeleteByID(id uint) error {
	return r.db.Delete(&model.UserToken{}, id).Error
}

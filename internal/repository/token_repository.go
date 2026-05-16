package repository

import (
	"dropoutbox/internal/model"

	"gorm.io/gorm"
)

type TokenRepository struct {
	db *gorm.DB
}

func NewTokenRepository(db *gorm.DB) *TokenRepository {
	return &TokenRepository{db: db}
}

func (r *TokenRepository) Create(token *model.Token) error {
	return r.db.Create(token).Error
}

func (r *TokenRepository) FindByAccess(access string) (*model.Token, error) {
	var token model.Token
	err := r.db.
		Preload("User.Roles", func(tx *gorm.DB) *gorm.DB { return tx.Order("roles.id asc") }).
		Preload("User.Roles.Permissions", func(tx *gorm.DB) *gorm.DB { return tx.Order("permissions.id asc") }).
		Where("access = ?", access).
		First(&token).Error
	if err != nil {
		return nil, err
	}
	return &token, nil
}

func (r *TokenRepository) FindByRefresh(refresh string) (*model.Token, error) {
	var token model.Token
	err := r.db.
		Preload("User.Roles", func(tx *gorm.DB) *gorm.DB { return tx.Order("roles.id asc") }).
		Preload("User.Roles.Permissions", func(tx *gorm.DB) *gorm.DB { return tx.Order("permissions.id asc") }).
		Where("refresh = ?", refresh).
		First(&token).Error
	if err != nil {
		return nil, err
	}
	return &token, nil
}

func (r *TokenRepository) DeleteByID(id uint) error {
	return r.db.Delete(&model.Token{}, id).Error
}

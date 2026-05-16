package repository

import (
	"dropoutbox/internal/model"

	"gorm.io/gorm"
)

type RoleRepository struct {
	db *gorm.DB
}

func NewRoleRepository(db *gorm.DB) *RoleRepository {
	return &RoleRepository{db: db}
}

func (r *RoleRepository) FindByIDs(roleIDs []uint) ([]model.Role, error) {
	if len(roleIDs) == 0 {
		return []model.Role{}, nil
	}

	var roles []model.Role
	err := r.db.Where("id IN ?", roleIDs).Order("id asc").Find(&roles).Error
	return roles, err
}

func (r *RoleRepository) Create(role *model.Role) error {
	return r.db.Create(role).Error
}

func (r *RoleRepository) FindByID(id uint) (*model.Role, error) {
	var role model.Role
	err := r.preloadDetails(r.db).First(&role, id).Error
	if err != nil {
		return nil, err
	}
	return &role, nil
}

func (r *RoleRepository) List(page, perPage int) ([]model.Role, int64, error) {
	var total int64
	if err := r.db.Model(&model.Role{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var roles []model.Role
	err := r.preloadDetails(r.db).
		Order("roles.id asc").
		Limit(perPage).
		Offset((page - 1) * perPage).
		Find(&roles).Error
	if err != nil {
		return nil, 0, err
	}

	return roles, total, nil
}

func (r *RoleRepository) Update(role *model.Role) error {
	return r.db.Save(role).Error
}

func (r *RoleRepository) ReplacePermissions(roleID uint, permissions []model.Permission) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("role_id = ?", roleID).Delete(&model.Permission{}).Error; err != nil {
			return err
		}
		if len(permissions) == 0 {
			return nil
		}
		return tx.Create(&permissions).Error
	})
}

func (r *RoleRepository) preloadDetails(db *gorm.DB) *gorm.DB {
	return db.Preload("Permissions", func(tx *gorm.DB) *gorm.DB {
		return tx.Order("permissions.id asc")
	})
}

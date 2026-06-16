package repository

import (
	"replica/internal/model"

	"gorm.io/gorm"
)

type UserRepository struct {
	db *gorm.DB
}

func NewUserRepository(db *gorm.DB) *UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) Create(user *model.User) error {
	return r.db.Create(user).Error
}

func (r *UserRepository) FindByID(id uint) (*model.User, error) {
	var user model.User
	err := r.preloadDetails(r.db).First(&user, id).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepository) FindByName(name string) (*model.User, error) {
	var user model.User
	err := r.preloadDetails(r.db).Where("name = ?", name).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepository) List(page, perPage int) ([]model.User, int64, error) {
	var total int64
	if err := r.db.Model(&model.User{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var users []model.User
	err := r.preloadDetails(r.db).
		Order("users.id asc").
		Limit(perPage).
		Offset((page - 1) * perPage).
		Find(&users).Error
	if err != nil {
		return nil, 0, err
	}

	return users, total, nil
}

func (r *UserRepository) Update(user *model.User) error {
	return r.db.Save(user).Error
}

func (r *UserRepository) Upsert(user *model.User) error {
	return r.db.Where("name = ?", user.Name).Assign(user).FirstOrCreate(user).Error
}

func (r *UserRepository) SetRoles(userID uint, roleIDs []uint) error {
	user := model.User{ID: userID}
	roles := make([]model.Role, len(roleIDs))
	for i, roleID := range roleIDs {
		roles[i] = model.Role{ID: roleID}
	}
	return r.db.Model(&user).Association("Roles").Replace(roles)
}

func (r *UserRepository) GetInventoryPermissions(userID uint, inventoryID uint) ([]model.InventoryPermission, error) {
	var permissions []model.InventoryPermission

	err := r.db.Joins("JOIN inventory_users iu ON iu.id = inventory_permissions.inventory_user_id").
		Where("iu.user_id = ? AND iu.inventory_id = ?", userID, inventoryID).
		Find(&permissions).Error

	return permissions, err
}

func (r *UserRepository) GetSharePermissions(userID uint, shareID uint) ([]model.SharePermission, error) {
	var permissions []model.SharePermission

	err := r.db.Joins("JOIN share_users su ON su.id = share_permissions.share_user_id").
		Where("su.user_id = ? AND su.share_id = ?", userID, shareID).
		Find(&permissions).Error

	return permissions, err
}

func (r *UserRepository) preloadDetails(db *gorm.DB) *gorm.DB {
	return db.Preload("Roles", func(tx *gorm.DB) *gorm.DB {
		return tx.Order("roles.id asc")
	}).Preload("Roles.Permissions", func(tx *gorm.DB) *gorm.DB {
		return tx.Order("permissions.id asc")
	})
}

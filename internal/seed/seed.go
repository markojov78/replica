package seed

import (
	"replica/internal/config"
	"replica/internal/model"
	"replica/internal/repository"
	"replica/internal/security"
	"replica/internal/service"

	"gorm.io/gorm"
)

func Run(db *gorm.DB, cfg config.SeedConfig) error {
	settingService := service.NewSettingService(repository.NewSettingRepository(db))
	if err := settingService.EnsureTransferKeys(); err != nil {
		return err
	}

	adminName := cfg.AdminName
	if adminName == "" {
		adminName = "admin"
	}

	adminPassword := cfg.AdminPassword
	if adminPassword == "" {
		adminPassword = "change-me"
	}

	hashedPassword, err := security.HashPassword(adminPassword)
	if err != nil {
		return err
	}

	admin := model.User{
		Name:     adminName,
		Status:   "active",
		Password: hashedPassword,
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("name = ?", admin.Name).Assign(admin).FirstOrCreate(&admin).Error; err != nil {
			return err
		}

		role := model.Role{
			Name:        "Admin",
			Description: "Application administrator with full access",
			Status:      model.RoleStatusActive,
		}
		if err := tx.Where("name = ?", role.Name).Assign(role).FirstOrCreate(&role).Error; err != nil {
			return err
		}

		if err := tx.Where("role_id = ?", role.ID).Delete(&model.Permission{}).Error; err != nil {
			return err
		}

		permissions := []model.Permission{
			{RoleID: role.ID, Resource: model.PermissionResourceUsers, Action: model.PermissionActionRead},
			{RoleID: role.ID, Resource: model.PermissionResourceUsers, Action: model.PermissionActionCreate},
			{RoleID: role.ID, Resource: model.PermissionResourceUsers, Action: model.PermissionActionUpdate},
			{RoleID: role.ID, Resource: model.PermissionResourceUsers, Action: model.PermissionActionDelete},
			{RoleID: role.ID, Resource: model.PermissionResourceShares, Action: model.PermissionActionRead},
			{RoleID: role.ID, Resource: model.PermissionResourceShares, Action: model.PermissionActionCreate},
			{RoleID: role.ID, Resource: model.PermissionResourceShares, Action: model.PermissionActionUpdate},
			{RoleID: role.ID, Resource: model.PermissionResourceShares, Action: model.PermissionActionDelete},
			{RoleID: role.ID, Resource: model.PermissionResourceInventories, Action: model.PermissionActionRead},
			{RoleID: role.ID, Resource: model.PermissionResourceInventories, Action: model.PermissionActionCreate},
			{RoleID: role.ID, Resource: model.PermissionResourceInventories, Action: model.PermissionActionUpdate},
			{RoleID: role.ID, Resource: model.PermissionResourceInventories, Action: model.PermissionActionDelete},
			{RoleID: role.ID, Resource: model.PermissionResourceNodes, Action: model.PermissionActionRead},
			{RoleID: role.ID, Resource: model.PermissionResourceNodes, Action: model.PermissionActionCreate},
			{RoleID: role.ID, Resource: model.PermissionResourceNodes, Action: model.PermissionActionUpdate},
			{RoleID: role.ID, Resource: model.PermissionResourceNodes, Action: model.PermissionActionDelete},
			{RoleID: role.ID, Resource: model.PermissionResourceSettings, Action: model.PermissionActionRead},
			{RoleID: role.ID, Resource: model.PermissionResourceSettings, Action: model.PermissionActionUpdate},
		}
		if err := tx.Create(&permissions).Error; err != nil {
			return err
		}

		userRole := model.UserRole{UserID: admin.ID, RoleID: role.ID}
		return tx.Where("user_id = ? AND role_id = ?", admin.ID, role.ID).FirstOrCreate(&userRole).Error
	})
}

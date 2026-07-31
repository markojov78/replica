package seed

import (
	"path/filepath"
	"testing"

	"replica/internal/config"
	"replica/internal/db"
	"replica/internal/model"
)

func TestRunAddsAdminNodePermissions(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "seed.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	if err := Run(database, config.SeedConfig{AdminName: "admin", AdminPassword: "secret"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var role model.Role
	if err := database.First(&role, "name = ?", "Admin").Error; err != nil {
		t.Fatalf("First(role) error = %v", err)
	}

	var permissions []model.Permission
	if err := database.Where("role_id = ?", role.ID).Order("id asc").Find(&permissions).Error; err != nil {
		t.Fatalf("Find(permissions) error = %v", err)
	}

	required := map[model.PermissionAction]bool{
		model.PermissionActionRead:   false,
		model.PermissionActionCreate: false,
		model.PermissionActionUpdate: false,
		model.PermissionActionDelete: false,
	}

	for _, permission := range permissions {
		if permission.Resource == model.PermissionResourceNodes {
			required[permission.Action] = true
		}
	}

	for action, found := range required {
		if !found {
			t.Fatalf("missing admin nodes permission for action %q", action)
		}
	}

	settingsRequired := map[model.PermissionAction]bool{
		model.PermissionActionRead:   false,
		model.PermissionActionUpdate: false,
	}
	for _, permission := range permissions {
		if permission.Resource == model.PermissionResourceSettings {
			settingsRequired[permission.Action] = true
		}
	}
	for action, found := range settingsRequired {
		if !found {
			t.Fatalf("missing admin settings permission for action %q", action)
		}
	}
}

func TestRunPreservesExistingAdminRecordsAndLeavesThemUnlinked(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "seed.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	admin := model.User{
		Username: "admin",
		Status:   model.UserStatusDeleted,
		Password: "existing-password-hash",
	}
	if err := database.Create(&admin).Error; err != nil {
		t.Fatalf("Create(admin) error = %v", err)
	}

	role := model.Role{
		Name:        "Admin",
		Description: "Customized administrator role",
		Status:      model.RoleStatusDeleted,
	}
	if err := database.Create(&role).Error; err != nil {
		t.Fatalf("Create(role) error = %v", err)
	}

	permission := model.Permission{
		RoleID:   role.ID,
		Resource: model.PermissionResourceShares,
		Action:   model.PermissionActionRead,
	}
	if err := database.Create(&permission).Error; err != nil {
		t.Fatalf("Create(permission) error = %v", err)
	}

	if err := Run(database, config.SeedConfig{AdminName: "admin", AdminPassword: "new-password"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var gotAdmin model.User
	if err := database.First(&gotAdmin, admin.ID).Error; err != nil {
		t.Fatalf("First(admin) error = %v", err)
	}
	if gotAdmin.Status != admin.Status || gotAdmin.Password != admin.Password {
		t.Fatalf("admin = %#v, want status %q and password %q", gotAdmin, admin.Status, admin.Password)
	}

	var gotRole model.Role
	if err := database.First(&gotRole, role.ID).Error; err != nil {
		t.Fatalf("First(role) error = %v", err)
	}
	if gotRole.Description != role.Description || gotRole.Status != role.Status {
		t.Fatalf("role = %#v, want description %q and status %q", gotRole, role.Description, role.Status)
	}

	var permissions []model.Permission
	if err := database.Where("role_id = ?", role.ID).Find(&permissions).Error; err != nil {
		t.Fatalf("Find(permissions) error = %v", err)
	}
	if len(permissions) != 1 || permissions[0].ID != permission.ID {
		t.Fatalf("permissions = %#v, want only existing permission %#v", permissions, permission)
	}

	var userRoleCount int64
	if err := database.Model(&model.UserRole{}).
		Where("user_id = ? AND role_id = ?", admin.ID, role.ID).
		Count(&userRoleCount).Error; err != nil {
		t.Fatalf("Count(user role) error = %v", err)
	}
	if userRoleCount != 0 {
		t.Fatalf("user role count = %d, want 0", userRoleCount)
	}
}

func TestRunLinksExistingAdminWhenRoleIsCreated(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "seed.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	admin := model.User{
		Username: "admin",
		Status:   model.UserStatusActive,
		Password: "existing-password-hash",
	}
	if err := database.Create(&admin).Error; err != nil {
		t.Fatalf("Create(admin) error = %v", err)
	}

	if err := Run(database, config.SeedConfig{AdminName: "admin", AdminPassword: "new-password"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var role model.Role
	if err := database.First(&role, "name = ?", "Admin").Error; err != nil {
		t.Fatalf("First(role) error = %v", err)
	}

	var userRole model.UserRole
	if err := database.First(&userRole, "user_id = ? AND role_id = ?", admin.ID, role.ID).Error; err != nil {
		t.Fatalf("First(user role) error = %v", err)
	}
}

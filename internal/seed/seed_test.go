package seed

import (
	"path/filepath"
	"testing"

	"dropoutbox/internal/config"
	"dropoutbox/internal/db"
	"dropoutbox/internal/model"
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
}

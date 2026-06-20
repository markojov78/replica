package service

import (
	"errors"
	"path/filepath"
	"testing"

	"replica/internal/config"
	"replica/internal/db"
	"replica/internal/model"
	"replica/internal/repository"
)

func TestRoleServiceSettingsPermissions(t *testing.T) {
	database, err := db.Open(config.DatabaseConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "roles.db"),
	})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}

	service := NewRoleService(repository.NewRoleRepository(database))
	role, err := service.Create("settings-manager", "", []RolePermissionInput{
		{Resource: string(model.PermissionResourceSettings), Action: string(model.PermissionActionRead)},
		{Resource: string(model.PermissionResourceSettings), Action: string(model.PermissionActionUpdate)},
	})
	if err != nil {
		t.Fatalf("Create(settings read/update) error = %v", err)
	}
	if len(role.Permissions) != 2 {
		t.Fatalf("len(role.Permissions) = %d, want 2", len(role.Permissions))
	}

	_, err = service.Create("invalid-settings-manager", "", []RolePermissionInput{
		{Resource: string(model.PermissionResourceSettings), Action: string(model.PermissionActionCreate)},
	})
	if !errors.Is(err, ErrInvalidPermissions) {
		t.Fatalf("Create(settings create) error = %v, want %v", err, ErrInvalidPermissions)
	}
}

package seed

import (
	"dropoutbox/internal/config"
	"dropoutbox/internal/model"

	"gorm.io/gorm"
)

func Run(db *gorm.DB, cfg config.SeedConfig) error {
	adminName := cfg.AdminName
	if adminName == "" {
		adminName = "admin"
	}

	adminPassword := cfg.AdminPassword
	if adminPassword == "" {
		adminPassword = "change-me"
	}

	admin := model.User{
		Name:     adminName,
		Status:   "active",
		Password: adminPassword,
	}

	return db.Where("name = ?", admin.Name).Assign(admin).FirstOrCreate(&admin).Error
}

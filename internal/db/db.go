package db

import (
	"fmt"
	"log"
	"os"
	"time"

	"dropoutbox/internal/config"
	"dropoutbox/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func Open(cfg config.DatabaseConfig) (*gorm.DB, error) {
	dialector, err := dialector(cfg)
	if err != nil {
		return nil, err
	}

	return gorm.Open(dialector, &gorm.Config{
		Logger: logger.New(
			log.New(os.Stdout, "\r\n", log.LstdFlags),
			logger.Config{
				SlowThreshold:             time.Second,
				LogLevel:                  logger.Warn,
				IgnoreRecordNotFoundError: true,
				Colorful:                  false,
			},
		),
		NamingStrategy: schema.NamingStrategy{
			SingularTable: false,
		},
	})
}

func AutoMigrate(database *gorm.DB) error {
	return database.AutoMigrate(model.AllModels()...)
}

func dialector(cfg config.DatabaseConfig) (gorm.Dialector, error) {
	switch cfg.Driver {
	case "sqlite":
		return sqlite.Open(cfg.DSN), nil
	case "postgres":
		return postgres.Open(cfg.DSN), nil
	default:
		return nil, fmt.Errorf("unsupported database driver %q", cfg.Driver)
	}
}

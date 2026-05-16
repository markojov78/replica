package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	App      AppConfig
	Auth     AuthConfig
	HTTP     HTTPConfig
	Database DatabaseConfig
	Seed     SeedConfig
}

type AppConfig struct {
	Name        string
	NodeID      string
	Coordinator bool
	Storage     bool
}

type AuthConfig struct {
	AccessTokenDuration  time.Duration
	RefreshTokenDuration time.Duration
}

type HTTPConfig struct {
	Address string
}

type DatabaseConfig struct {
	Driver      string
	DSN         string
	AutoMigrate bool
}

type SeedConfig struct {
	AdminName     string
	AdminPassword string
}

type rawConfig struct {
	App      rawAppConfig      `json:"app" yaml:"app" toml:"app"`
	Auth     rawAuthConfig     `json:"auth" yaml:"auth" toml:"auth"`
	HTTP     rawHTTPConfig     `json:"http" yaml:"http" toml:"http"`
	Database rawDatabaseConfig `json:"database" yaml:"database" toml:"database"`
	Seed     rawSeedConfig     `json:"seed" yaml:"seed" toml:"seed"`
}

type rawAppConfig struct {
	Name        *string `json:"name" yaml:"name" toml:"name"`
	NodeID      *string `json:"node_id" yaml:"node_id" toml:"node_id"`
	Coordinator *bool   `json:"coordinator" yaml:"coordinator" toml:"coordinator"`
	Storage     *bool   `json:"storage" yaml:"storage" toml:"storage"`
}

type rawAuthConfig struct {
	AccessTokenDuration  *string `json:"access_token_duration" yaml:"access_token_duration" toml:"access_token_duration"`
	RefreshTokenDuration *string `json:"refresh_token_duration" yaml:"refresh_token_duration" toml:"refresh_token_duration"`
}

type rawHTTPConfig struct {
	Address *string `json:"address" yaml:"address" toml:"address"`
}

type rawDatabaseConfig struct {
	Driver      *string `json:"driver" yaml:"driver" toml:"driver"`
	DSN         *string `json:"dsn" yaml:"dsn" toml:"dsn"`
	AutoMigrate *bool   `json:"auto_migrate" yaml:"auto_migrate" toml:"auto_migrate"`
}

type rawSeedConfig struct {
	AdminName     *string `json:"admin_name" yaml:"admin_name" toml:"admin_name"`
	AdminPassword *string `json:"admin_password" yaml:"admin_password" toml:"admin_password"`
}

var defaultConfigFiles = []string{
	"config.json",
	"config.yaml",
	"config.yml",
	"config.toml",
}

func Load() (Config, error) {
	fileCfg, err := loadFileConfig()
	if err != nil {
		return Config{}, err
	}

	driver := resolveString("DB_DRIVER", fileCfg.Database.Driver, "sqlite")

	cfg := Config{
		App: AppConfig{
			Name:        resolveString("APP_NAME", fileCfg.App.Name, "dropoutbox"),
			NodeID:      resolveString("APP_NODE_ID", fileCfg.App.NodeID, "node-1"),
			Coordinator: resolveBool("APP_COORDINATOR", fileCfg.App.Coordinator, true),
			Storage:     resolveBool("APP_STORAGE", fileCfg.App.Storage, true),
		},
		Auth: AuthConfig{
			AccessTokenDuration:  resolveDuration("AUTH_ACCESS_TOKEN_DURATION", fileCfg.Auth.AccessTokenDuration, 30*time.Minute),
			RefreshTokenDuration: resolveDuration("AUTH_REFRESH_TOKEN_DURATION", fileCfg.Auth.RefreshTokenDuration, 8*time.Hour),
		},
		HTTP: HTTPConfig{
			Address: resolveString("HTTP_ADDR", fileCfg.HTTP.Address, ":8080"),
		},
		Database: DatabaseConfig{
			Driver:      driver,
			DSN:         resolveDSN(fileCfg.Database.DSN, driver),
			AutoMigrate: resolveBool("DB_AUTO_MIGRATE", fileCfg.Database.AutoMigrate, true),
		},
		Seed: SeedConfig{
			AdminName:     resolveString("SEED_ADMIN_NAME", fileCfg.Seed.AdminName, "admin"),
			AdminPassword: resolveString("SEED_ADMIN_PASSWORD", fileCfg.Seed.AdminPassword, "change-me"),
		},
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func defaultDSN(driver string) string {
	switch driver {
	case "postgres":
		return "host=localhost user=postgres password=postgres dbname=dropoutbox port=5432 sslmode=disable"
	default:
		return "dropoutbox.db"
	}
}

func loadFileConfig() (rawConfig, error) {
	path, err := resolveConfigFile()
	if err != nil {
		return rawConfig{}, err
	}
	if path == "" {
		return rawConfig{}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return rawConfig{}, fmt.Errorf("read config file %q: %w", path, err)
	}

	var cfg rawConfig
	switch ext := filepath.Ext(path); ext {
	case ".json":
		err = json.Unmarshal(data, &cfg)
	case ".yaml", ".yml":
		err = yaml.Unmarshal(data, &cfg)
	case ".toml":
		err = toml.Unmarshal(data, &cfg)
	default:
		return rawConfig{}, fmt.Errorf("unsupported config file format %q", ext)
	}
	if err != nil {
		return rawConfig{}, fmt.Errorf("parse config file %q: %w", path, err)
	}

	return cfg, nil
}

func resolveConfigFile() (string, error) {
	if path, ok := os.LookupEnv("CONFIG_FILE"); ok && path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("stat config file %q: %w", path, err)
		}
		return path, nil
	}

	for _, candidate := range defaultConfigFiles {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("stat config file %q: %w", candidate, err)
		}
	}

	return "", nil
}

func resolveString(key string, fileValue *string, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return strings.TrimSpace(value)
	}
	if fileValue != nil && *fileValue != "" {
		return strings.TrimSpace(*fileValue)
	}
	return strings.TrimSpace(fallback)
}

func resolveDSN(fileValue *string, driver string) string {
	if value, ok := os.LookupEnv("DB_DSN"); ok && value != "" {
		return value
	}
	if fileValue != nil && *fileValue != "" {
		return *fileValue
	}
	return defaultDSN(driver)
}

func resolveBool(key string, fileValue *bool, fallback bool) bool {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		if fileValue != nil {
			return *fileValue
		}
		return fallback
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		if fileValue != nil {
			return *fileValue
		}
		return fallback
	}

	return parsed
}

func resolveDuration(key string, fileValue *string, fallback time.Duration) time.Duration {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		if parsed, err := time.ParseDuration(strings.TrimSpace(value)); err == nil {
			return parsed
		}
		if fileValue != nil {
			if parsed, err := time.ParseDuration(strings.TrimSpace(*fileValue)); err == nil {
				return parsed
			}
		}
		return fallback
	}

	if fileValue != nil && *fileValue != "" {
		if parsed, err := time.ParseDuration(strings.TrimSpace(*fileValue)); err == nil {
			return parsed
		}
	}

	return fallback
}

func (c Config) Validate() error {
	if c.App.Name == "" {
		return errors.New("app.name is required")
	}
	if c.App.NodeID == "" {
		return errors.New("app.node_id is required")
	}
	if !c.App.Coordinator && !c.App.Storage {
		return errors.New("at least one of app.coordinator or app.storage must be true")
	}
	if c.HTTP.Address == "" {
		return errors.New("http.address is required")
	}
	if c.Auth.AccessTokenDuration <= 0 {
		return errors.New("auth.access_token_duration must be greater than 0")
	}
	if c.Auth.RefreshTokenDuration <= 0 {
		return errors.New("auth.refresh_token_duration must be greater than 0")
	}
	if c.Database.Driver == "" {
		return errors.New("database.driver is required")
	}
	if c.Database.DSN == "" {
		return errors.New("database.dsn is required")
	}
	switch c.Database.Driver {
	case "sqlite", "postgres":
	default:
		return fmt.Errorf("unsupported database.driver %q", c.Database.Driver)
	}
	return nil
}

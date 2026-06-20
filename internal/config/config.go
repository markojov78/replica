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
	Sharing  SharingConfig
	Auth     AuthConfig
	HTTP     HTTPConfig
	Database DatabaseConfig
	Seed     SeedConfig
}

type AppConfig struct {
	NodeID              string
	Coordinator         bool
	Storage             bool
	CoordinatorURL      string
	NodeAddress         string
	HeartbeatInterval   time.Duration
	APIRequestTimeout   time.Duration
	FileTransferTimeout time.Duration
}

type SharingConfig struct {
	ThumbnailSizes             []int
	ThumbnailDefaultSize       int
	ThumbnailsGenerateForVideo bool
	FfmpegPath                 string
	VideoInlineMaxSizeMB       int
	VideoPlaybackEnabled       bool
	ThumbnailStorage           string
	ThumbnailStorageLimitMB    int
}

type AuthConfig struct {
	JWTSecret                  string
	NodeSecret                 string
	AccessTokenDuration        time.Duration
	RefreshTokenDuration       time.Duration
	ShareAPITokenCacheDuration time.Duration
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
	Sharing  rawSharingConfig  `json:"sharing" yaml:"sharing" toml:"sharing"`
	Auth     rawAuthConfig     `json:"auth" yaml:"auth" toml:"auth"`
	HTTP     rawHTTPConfig     `json:"http" yaml:"http" toml:"http"`
	Database rawDatabaseConfig `json:"database" yaml:"database" toml:"database"`
	Seed     rawSeedConfig     `json:"seed" yaml:"seed" toml:"seed"`
}

type rawAppConfig struct {
	NodeID              *string `json:"node_id" yaml:"node_id" toml:"node_id"`
	Coordinator         *bool   `json:"coordinator" yaml:"coordinator" toml:"coordinator"`
	Storage             *bool   `json:"storage" yaml:"storage" toml:"storage"`
	CoordinatorURL      *string `json:"coordinator_url" yaml:"coordinator_url" toml:"coordinator_url"`
	NodeAddress         *string `json:"node_address" yaml:"node_address" toml:"node_address"`
	HeartbeatInterval   *string `json:"heartbeat_interval" yaml:"heartbeat_interval" toml:"heartbeat_interval"`
	APIRequestTimeout   *string `json:"api_request_timeout" yaml:"api_request_timeout" toml:"api_request_timeout"`
	FileTransferTimeout *string `json:"file_transfer_timeout" yaml:"file_transfer_timeout" toml:"file_transfer_timeout"`
}

type rawSharingConfig struct {
	ThumbnailSizes             *[]int  `json:"thumbnail_sizes" yaml:"thumbnail_sizes" toml:"thumbnail_sizes"`
	ThumbnailDefaultSize       *int    `json:"thumbnail_default_size" yaml:"thumbnail_default_size" toml:"thumbnail_default_size"`
	ThumbnailsGenerateForVideo *bool   `json:"thumbnails_generate_for_video" yaml:"thumbnails_generate_for_video" toml:"thumbnails_generate_for_video"`
	FfmpegPath                 *string `json:"ffmpeg_path" yaml:"ffmpeg_path" toml:"ffmpeg_path"`
	VideoInlineMaxSizeMB       *int    `json:"video_inline_max_size_mb" yaml:"video_inline_max_size_mb" toml:"video_inline_max_size_mb"`
	VideoPlaybackEnabled       *bool   `json:"video_playback_enabled" yaml:"video_playback_enabled" toml:"video_playback_enabled"`
	ThumbnailStorage           *string `json:"thumbnail_storage" yaml:"thumbnail_storage" toml:"thumbnail_storage"`
	ThumbnailStorageLimitMB    *int    `json:"thumbnail_storage_limit_mb" yaml:"thumbnail_storage_limit_mb" toml:"thumbnail_storage_limit_mb"`
}

type rawAuthConfig struct {
	JWTSecret                  *string `json:"jwt_secret" yaml:"jwt_secret" toml:"jwt_secret"`
	NodeSecret                 *string `json:"node_secret" yaml:"node_secret" toml:"node_secret"`
	AccessTokenDuration        *string `json:"access_token_duration" yaml:"access_token_duration" toml:"access_token_duration"`
	RefreshTokenDuration       *string `json:"refresh_token_duration" yaml:"refresh_token_duration" toml:"refresh_token_duration"`
	ShareAPITokenCacheDuration *string `json:"share_api_token_cache_duration" yaml:"share_api_token_cache_duration" toml:"share_api_token_cache_duration"`
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

const (
	SettingSharingThumbnailSizes             = "sharing.thumbnails.sizes"
	SettingSharingThumbnailDefaultSize       = "sharing.thumbnail_default_size"
	SettingSharingThumbnailsGenerateForVideo = "sharing.thumbnails_generate_for_video"
	SettingSharingVideoInlineMaxSizeMB       = "sharing.video_inline_max_size_mb"
	SettingSharingVideoPlaybackEnabled       = "sharing.video_playback_enabled"
)

func Load() (Config, error) {
	fileCfg, err := loadFileConfig()
	if err != nil {
		return Config{}, err
	}

	driver := resolveString("DB_DRIVER", fileCfg.Database.Driver, "sqlite")

	cfg := Config{
		App: AppConfig{
			NodeID:              resolveString("APP_NODE_ID", fileCfg.App.NodeID, "node-1"),
			Coordinator:         resolveBool("APP_COORDINATOR", fileCfg.App.Coordinator, true),
			Storage:             resolveBool("APP_STORAGE", fileCfg.App.Storage, true),
			CoordinatorURL:      resolveString("APP_COORDINATOR_URL", fileCfg.App.CoordinatorURL, ""),
			NodeAddress:         resolveString("APP_NODE_ADDRESS", fileCfg.App.NodeAddress, ""),
			HeartbeatInterval:   resolveDuration("APP_HEARTBEAT_INTERVAL", fileCfg.App.HeartbeatInterval, 10*time.Minute),
			APIRequestTimeout:   resolveDuration("APP_API_REQUEST_TIMEOUT", fileCfg.App.APIRequestTimeout, 15*time.Second),
			FileTransferTimeout: resolveDuration("APP_FILE_TRANSFER_TIMEOUT", fileCfg.App.FileTransferTimeout, 30*time.Minute),
		},
		Sharing: SharingConfig{
			ThumbnailSizes:             resolveIntSlice("SHARING_THUMBNAIL_SIZES", fileCfg.Sharing.ThumbnailSizes, []int{256, 512, 1024}),
			ThumbnailDefaultSize:       resolveInt("SHARING_THUMBNAIL_DEFAULT_SIZE", fileCfg.Sharing.ThumbnailDefaultSize, 256),
			ThumbnailsGenerateForVideo: resolveBool("SHARING_THUMBNAILS_GENERATE_FOR_VIDEO", fileCfg.Sharing.ThumbnailsGenerateForVideo, true),
			FfmpegPath:                 resolveString("SHARING_FFMPEG_PATH", fileCfg.Sharing.FfmpegPath, "ffmpeg"),
			VideoInlineMaxSizeMB:       resolveInt("SHARING_VIDEO_INLINE_MAX_SIZE_MB", fileCfg.Sharing.VideoInlineMaxSizeMB, 25),
			VideoPlaybackEnabled:       resolveBool("SHARING_VIDEO_PLAYBACK_ENABLED", fileCfg.Sharing.VideoPlaybackEnabled, true),
			ThumbnailStorage:           resolveString("SHARING_THUMBNAIL_STORAGE", fileCfg.Sharing.ThumbnailStorage, "/tmp/replica_thumbnails"),
			ThumbnailStorageLimitMB:    resolveInt("SHARING_THUMBNAIL_STORAGE_LIMIT_MB", fileCfg.Sharing.ThumbnailStorageLimitMB, 500),
		},
		Auth: AuthConfig{
			JWTSecret:                  resolveString("AUTH_JWT_SECRET", fileCfg.Auth.JWTSecret, "change-me"),
			NodeSecret:                 resolveString("AUTH_NODE_SECRET", fileCfg.Auth.NodeSecret, ""),
			AccessTokenDuration:        resolveDuration("AUTH_ACCESS_TOKEN_DURATION", fileCfg.Auth.AccessTokenDuration, 30*time.Minute),
			RefreshTokenDuration:       resolveDuration("AUTH_REFRESH_TOKEN_DURATION", fileCfg.Auth.RefreshTokenDuration, 8*time.Hour),
			ShareAPITokenCacheDuration: resolveDuration("AUTH_SHARE_API_TOKEN_CACHE_DURATION", fileCfg.Auth.ShareAPITokenCacheDuration, 5*time.Minute),
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

func DatabaseSettingKeys() []string {
	return []string{
		SettingSharingThumbnailSizes,
		SettingSharingThumbnailDefaultSize,
		SettingSharingThumbnailsGenerateForVideo,
		SettingSharingVideoInlineMaxSizeMB,
		SettingSharingVideoPlaybackEnabled,
	}
}

func (c *Config) ApplyDatabaseSettings(settings map[string]string, logf func(string, ...any)) {
	for key, value := range settings {
		switch key {
		case SettingSharingThumbnailSizes:
			parsed, err := parseIntSlice(value)
			if err != nil {
				logInvalidDatabaseSetting(logf, key, value, err)
				continue
			}
			c.Sharing.ThumbnailSizes = parsed
		case SettingSharingThumbnailDefaultSize:
			parsed, err := parsePositiveInt(value)
			if err != nil {
				logInvalidDatabaseSetting(logf, key, value, err)
				continue
			}
			c.Sharing.ThumbnailDefaultSize = parsed
		case SettingSharingThumbnailsGenerateForVideo:
			parsed, err := strconv.ParseBool(strings.TrimSpace(value))
			if err != nil {
				logInvalidDatabaseSetting(logf, key, value, err)
				continue
			}
			c.Sharing.ThumbnailsGenerateForVideo = parsed
		case SettingSharingVideoInlineMaxSizeMB:
			parsed, err := parsePositiveInt(value)
			if err != nil {
				logInvalidDatabaseSetting(logf, key, value, err)
				continue
			}
			c.Sharing.VideoInlineMaxSizeMB = parsed
		case SettingSharingVideoPlaybackEnabled:
			parsed, err := strconv.ParseBool(strings.TrimSpace(value))
			if err != nil {
				logInvalidDatabaseSetting(logf, key, value, err)
				continue
			}
			c.Sharing.VideoPlaybackEnabled = parsed
		}
	}
}

func logInvalidDatabaseSetting(logf func(string, ...any), key string, value string, err error) {
	if logf == nil {
		return
	}
	logf("ignore invalid database setting %s=%q: %v", key, value, err)
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

func resolveInt(key string, fileValue *int, fallback int) int {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		if fileValue != nil {
			return *fileValue
		}
		return fallback
	}

	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		if fileValue != nil {
			return *fileValue
		}
		return fallback
	}

	return parsed
}

func resolveIntSlice(key string, fileValue *[]int, fallback []int) []int {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		if fileValue != nil {
			return append([]int(nil), (*fileValue)...)
		}
		return append([]int(nil), fallback...)
	}

	parsed, err := parseIntSlice(value)
	if err != nil {
		if fileValue != nil {
			return append([]int(nil), (*fileValue)...)
		}
		return append([]int(nil), fallback...)
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

func parseIntSlice(value string) ([]int, error) {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "[") {
		var parsed []int
		if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
			return nil, err
		}
		if len(parsed) == 0 {
			return nil, errors.New("must contain at least one value")
		}
		for _, item := range parsed {
			if item <= 0 {
				return nil, errors.New("values must be greater than 0")
			}
		}
		return parsed, nil
	}

	parts := strings.Split(value, ",")
	parsed := make([]int, 0, len(parts))
	for _, part := range parts {
		item, err := parsePositiveInt(part)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, item)
	}
	if len(parsed) == 0 {
		return nil, errors.New("must contain at least one value")
	}
	return parsed, nil
}

func parsePositiveInt(value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, err
	}
	if parsed <= 0 {
		return 0, errors.New("must be greater than 0")
	}
	return parsed, nil
}

func (c Config) Validate() error {
	if c.App.NodeID == "" {
		return errors.New("app.node_id is required")
	}
	if !c.App.Coordinator && !c.App.Storage {
		return errors.New("at least one of app.coordinator or app.storage must be true")
	}
	if c.App.Storage && c.App.HeartbeatInterval <= 0 {
		return errors.New("app.heartbeat_interval must be greater than 0 when storage is enabled")
	}
	if c.App.Storage && c.App.APIRequestTimeout <= 0 {
		return errors.New("app.api_request_timeout must be greater than 0 when storage is enabled")
	}
	if c.App.Storage && c.App.FileTransferTimeout <= 0 {
		return errors.New("app.file_transfer_timeout must be greater than 0 when storage is enabled")
	}

	if !c.App.Coordinator && c.App.Storage {
		if c.App.CoordinatorURL == "" {
			return errors.New("app.coordinator_url is required when storage is enabled without coordinator mode")
		}
		if c.App.NodeAddress == "" {
			return errors.New("app.node_address is required when storage is enabled without coordinator mode")
		}
		if c.Auth.NodeSecret == "" {
			return errors.New("auth.node_secret is required when storage is enabled without coordinator mode")
		}
		return nil
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
	if c.Auth.JWTSecret == "" {
		return errors.New("auth.jwt_secret is required")
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

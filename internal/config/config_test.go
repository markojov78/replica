package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLoadYAMLConfigWithEnvOverride(t *testing.T) {
	clearStorageProfileEnv(t)
	wd := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	defer func() {
		_ = os.Chdir(prev)
	}()
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("Chdir(%q) error = %v", wd, err)
	}

	configBody := `
app:
  node_id: coordinator-1
  coordinator: true
  storage: false
  coordinator_url: "http://coordinator:8080"
  node_address: "http://storage-1:8081"
  heartbeat_interval: "7m"
  api_request_timeout: "20s"
  file_transfer_timeout: "45m"
auth:
  jwt_secret: "file-secret"
  node_secret: "node-file-secret"
  access_token_duration: "45m"
  refresh_token_duration: "10h"
sharing:
  video_inline_max_size_mb: 20
  thumbnail_storage: "/var/cache/replica/thumbs"
  thumbnail_storage_limit_mb: 250
http:
  address: ":9090"
database:
  driver: postgres
  dsn: "host=db user=postgres password=postgres dbname=dropoutbox port=5432 sslmode=disable"
  auto_migrate: false
seed:
  admin_name: root
  admin_password: secret
storage:
  profiles:
    AWS:
      access_key_id: "file-access"
      secret_access_key: "file-secret-key"
      region: "us-east-1"
    backblaze:
      access_key_id: "b2-file-access"
      secret_access_key: "b2-file-secret"
      endpoint: "s3.eu-central-003.backblazeb2.com"
`
	if err := os.WriteFile(filepath.Join(wd, "config.yaml"), []byte(configBody), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("HTTP_ADDR", ":8088")
	t.Setenv("DB_AUTO_MIGRATE", "true")
	t.Setenv("APP_API_REQUEST_TIMEOUT", "25s")
	t.Setenv("SHARING_VIDEO_INLINE_MAX_SIZE_MB", "30")
	t.Setenv("SHARING_THUMBNAIL_STORAGE", "/env/replica/thumbs")
	t.Setenv("SHARING_THUMBNAIL_STORAGE_LIMIT_MB", "750")
	t.Setenv("STORAGE_PROFILES_AWS_REGION", "eu-west-1")
	t.Setenv("STORAGE_PROFILES_BACKBLAZE_ENDPOINT", "s3.us-west-004.backblazeb2.com")
	t.Setenv("STORAGE_PROFILES_ARCHIVE_ACCESS_KEY_ID", "archive-access")
	t.Setenv("STORAGE_PROFILES_ARCHIVE_SECRET_ACCESS_KEY", "archive-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTP.Address != ":8088" {
		t.Fatalf("HTTP.Address = %q, want %q", cfg.HTTP.Address, ":8088")
	}
	if cfg.Auth.JWTSecret != "file-secret" {
		t.Fatalf("Auth.JWTSecret = %q, want %q", cfg.Auth.JWTSecret, "file-secret")
	}
	if cfg.Auth.NodeSecret != "node-file-secret" {
		t.Fatalf("Auth.NodeSecret = %q, want %q", cfg.Auth.NodeSecret, "node-file-secret")
	}
	if cfg.App.CoordinatorURL != "http://coordinator:8080" {
		t.Fatalf("App.CoordinatorURL = %q, want %q", cfg.App.CoordinatorURL, "http://coordinator:8080")
	}
	if cfg.App.NodeAddress != "http://storage-1:8081" {
		t.Fatalf("App.NodeAddress = %q, want %q", cfg.App.NodeAddress, "http://storage-1:8081")
	}
	if cfg.App.HeartbeatInterval != 7*time.Minute {
		t.Fatalf("App.HeartbeatInterval = %s, want %s", cfg.App.HeartbeatInterval, 7*time.Minute)
	}
	if cfg.App.APIRequestTimeout != 25*time.Second {
		t.Fatalf("App.APIRequestTimeout = %s, want %s", cfg.App.APIRequestTimeout, 25*time.Second)
	}
	if cfg.App.FileTransferTimeout != 45*time.Minute {
		t.Fatalf("App.FileTransferTimeout = %s, want %s", cfg.App.FileTransferTimeout, 45*time.Minute)
	}
	if cfg.Sharing.VideoInlineMaxSizeMB != 30 {
		t.Fatalf("Sharing.VideoInlineMaxSizeMB = %d, want 30 from env override", cfg.Sharing.VideoInlineMaxSizeMB)
	}
	if cfg.Sharing.ThumbnailStorage != "/env/replica/thumbs" {
		t.Fatalf("Sharing.ThumbnailStorage = %q, want env override", cfg.Sharing.ThumbnailStorage)
	}
	if cfg.Sharing.ThumbnailStorageLimitMB != 750 {
		t.Fatalf("Sharing.ThumbnailStorageLimitMB = %d, want 750 from env override", cfg.Sharing.ThumbnailStorageLimitMB)
	}
	if cfg.Database.Driver != "postgres" {
		t.Fatalf("Database.Driver = %q, want %q", cfg.Database.Driver, "postgres")
	}
	if cfg.Database.DSN != "host=db user=postgres password=postgres dbname=dropoutbox port=5432 sslmode=disable" {
		t.Fatalf("Database.DSN = %q", cfg.Database.DSN)
	}
	if !cfg.Database.AutoMigrate {
		t.Fatal("Database.AutoMigrate = false, want true from env override")
	}
	if cfg.Seed.AdminName != "root" {
		t.Fatalf("Seed.AdminName = %q, want %q", cfg.Seed.AdminName, "root")
	}
	aws, ok := cfg.StorageProfile("aws")
	if !ok {
		t.Fatal("StorageProfile(aws) not found")
	}
	if aws.AccessKeyID != "file-access" {
		t.Fatalf("aws.AccessKeyID = %q, want file value", aws.AccessKeyID)
	}
	if aws.SecretAccessKey != "file-secret-key" {
		t.Fatalf("aws.SecretAccessKey = %q, want file value", aws.SecretAccessKey)
	}
	if aws.Region != "eu-west-1" {
		t.Fatalf("aws.Region = %q, want env override", aws.Region)
	}
	backblaze, ok := cfg.StorageProfile("BACKBLAZE")
	if !ok {
		t.Fatal("StorageProfile(BACKBLAZE) not found")
	}
	if backblaze.Endpoint != "s3.us-west-004.backblazeb2.com" {
		t.Fatalf("backblaze.Endpoint = %q, want env override", backblaze.Endpoint)
	}
	archive, ok := cfg.StorageProfile("archive")
	if !ok {
		t.Fatal("StorageProfile(archive) not found")
	}
	if archive.AccessKeyID != "archive-access" || archive.SecretAccessKey != "archive-secret" {
		t.Fatalf("archive profile = %+v, want env-only credentials", archive)
	}
}

func TestLoadFromExplicitTOMLConfigFile(t *testing.T) {
	clearStorageProfileEnv(t)
	wd := t.TempDir()
	configPath := filepath.Join(wd, "dropoutbox.toml")
	configBody := `
[app]
node_id = "storage-1"
coordinator = false
storage = true
coordinator_url = "http://coordinator:8080"
node_address = "http://storage-1:8081"
heartbeat_interval = "3m"
api_request_timeout = "12s"
file_transfer_timeout = "1h"

[auth]
jwt_secret = "toml-secret"
node_secret = "toml-node-secret"
access_token_duration = "15m"
refresh_token_duration = "6h"

[http]
address = ":8181"

[database]
driver = "sqlite"
dsn = "custom.db"
auto_migrate = false

[storage.profiles.aws]
access_key_id = "toml-access"
secret_access_key = "toml-secret-access"
region = "us-east-2"
`
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("CONFIG_FILE", configPath)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Database.Driver != "sqlite" {
		t.Fatalf("Database.Driver = %q, want %q", cfg.Database.Driver, "sqlite")
	}
	if cfg.App.CoordinatorURL != "http://coordinator:8080" {
		t.Fatalf("App.CoordinatorURL = %q, want %q", cfg.App.CoordinatorURL, "http://coordinator:8080")
	}
	if cfg.App.NodeAddress != "http://storage-1:8081" {
		t.Fatalf("App.NodeAddress = %q, want %q", cfg.App.NodeAddress, "http://storage-1:8081")
	}
	if cfg.App.HeartbeatInterval != 3*time.Minute {
		t.Fatalf("App.HeartbeatInterval = %s, want %s", cfg.App.HeartbeatInterval, 3*time.Minute)
	}
	if cfg.App.APIRequestTimeout != 12*time.Second {
		t.Fatalf("App.APIRequestTimeout = %s, want %s", cfg.App.APIRequestTimeout, 12*time.Second)
	}
	if cfg.App.FileTransferTimeout != time.Hour {
		t.Fatalf("App.FileTransferTimeout = %s, want %s", cfg.App.FileTransferTimeout, time.Hour)
	}
	if cfg.Auth.NodeSecret != "toml-node-secret" {
		t.Fatalf("Auth.NodeSecret = %q, want %q", cfg.Auth.NodeSecret, "toml-node-secret")
	}
	if cfg.Database.DSN != "custom.db" {
		t.Fatalf("Database.DSN = %q, want %q", cfg.Database.DSN, "custom.db")
	}
	if cfg.Database.AutoMigrate {
		t.Fatal("Database.AutoMigrate = true, want false")
	}
	aws, ok := cfg.StorageProfile("AWS")
	if !ok {
		t.Fatal("StorageProfile(AWS) not found")
	}
	if aws.Region != "us-east-2" {
		t.Fatalf("aws.Region = %q, want TOML value", aws.Region)
	}
}

func TestLoadRejectsMissingExplicitConfigFile(t *testing.T) {
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "missing.yaml"))

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error")
	}
}

func TestLoadAllowsMinimalStorageOnlyMode(t *testing.T) {
	clearStorageProfileEnv(t)
	t.Setenv("CONFIG_FILE", "")
	t.Setenv("APP_NODE_ID", "node-a")
	t.Setenv("APP_COORDINATOR", "false")
	t.Setenv("APP_STORAGE", "true")
	t.Setenv("APP_COORDINATOR_URL", "http://coordinator:8080")
	t.Setenv("APP_NODE_ADDRESS", "http://node-a:8081")
	t.Setenv("APP_HEARTBEAT_INTERVAL", "5m")
	t.Setenv("APP_API_REQUEST_TIMEOUT", "10s")
	t.Setenv("APP_FILE_TRANSFER_TIMEOUT", "2h")
	t.Setenv("AUTH_JWT_SECRET", "")
	t.Setenv("AUTH_NODE_SECRET", "node-secret")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("DB_DRIVER", "")
	t.Setenv("DB_DSN", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.App.NodeID != "node-a" {
		t.Fatalf("App.NodeID = %q, want %q", cfg.App.NodeID, "node-a")
	}
	if cfg.App.Coordinator {
		t.Fatal("App.Coordinator = true, want false")
	}
	if !cfg.App.Storage {
		t.Fatal("App.Storage = false, want true")
	}
	if cfg.App.HeartbeatInterval != 5*time.Minute {
		t.Fatalf("App.HeartbeatInterval = %s, want %s", cfg.App.HeartbeatInterval, 5*time.Minute)
	}
	if cfg.App.APIRequestTimeout != 10*time.Second {
		t.Fatalf("App.APIRequestTimeout = %s, want %s", cfg.App.APIRequestTimeout, 10*time.Second)
	}
	if cfg.App.FileTransferTimeout != 2*time.Hour {
		t.Fatalf("App.FileTransferTimeout = %s, want %s", cfg.App.FileTransferTimeout, 2*time.Hour)
	}
}

func clearStorageProfileEnv(t *testing.T) {
	t.Helper()
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if ok && strings.HasPrefix(key, "STORAGE_PROFILES_") {
			t.Setenv(key, "")
		}
	}
}

func TestApplyDatabaseSettingsOverridesAllowedSharingValues(t *testing.T) {
	cfg := Config{
		Sharing: SharingConfig{
			ThumbnailSizes:             []int{128},
			ThumbnailDefaultSize:       128,
			ThumbnailsGenerateForVideo: false,
			FfmpegPath:                 "ffmpeg-custom",
			VideoInlineMaxSizeMB:       10,
			VideoPlaybackEnabled:       false,
		},
	}

	cfg.ApplyDatabaseSettings(map[string]string{
		SettingSharingThumbnailSizes:             "[256,384,512]",
		SettingSharingThumbnailDefaultSize:       "384",
		SettingSharingThumbnailsGenerateForVideo: "true",
		"sharing.ffmpeg_path":                    "ignored",
		SettingSharingVideoInlineMaxSizeMB:       "25",
		SettingSharingVideoPlaybackEnabled:       "true",
	}, nil)

	if !slices.Equal(cfg.Sharing.ThumbnailSizes, []int{256, 384, 512}) {
		t.Fatalf("Sharing.ThumbnailSizes = %v", cfg.Sharing.ThumbnailSizes)
	}
	if cfg.Sharing.ThumbnailDefaultSize != 384 {
		t.Fatalf("Sharing.ThumbnailDefaultSize = %d, want 384", cfg.Sharing.ThumbnailDefaultSize)
	}
	if !cfg.Sharing.ThumbnailsGenerateForVideo {
		t.Fatal("Sharing.ThumbnailsGenerateForVideo = false, want true")
	}
	if cfg.Sharing.FfmpegPath != "ffmpeg-custom" {
		t.Fatalf("Sharing.FfmpegPath = %q, want unchanged", cfg.Sharing.FfmpegPath)
	}
	if cfg.Sharing.VideoInlineMaxSizeMB != 25 {
		t.Fatalf("Sharing.VideoInlineMaxSizeMB = %d, want 25", cfg.Sharing.VideoInlineMaxSizeMB)
	}
	if !cfg.Sharing.VideoPlaybackEnabled {
		t.Fatal("Sharing.VideoPlaybackEnabled = false, want true")
	}
}

func TestApplyDatabaseSettingsIgnoresInvalidValues(t *testing.T) {
	cfg := Config{
		Sharing: SharingConfig{
			ThumbnailSizes:             []int{256},
			ThumbnailDefaultSize:       256,
			ThumbnailsGenerateForVideo: true,
			VideoInlineMaxSizeMB:       25,
			VideoPlaybackEnabled:       true,
		},
	}
	var logs []string

	cfg.ApplyDatabaseSettings(map[string]string{
		SettingSharingThumbnailSizes:             "256,nope",
		SettingSharingThumbnailDefaultSize:       "-1",
		SettingSharingThumbnailsGenerateForVideo: "sometimes",
		SettingSharingVideoInlineMaxSizeMB:       "nope",
		SettingSharingVideoPlaybackEnabled:       "maybe",
	}, func(format string, args ...any) {
		logs = append(logs, format)
	})

	if !slices.Equal(cfg.Sharing.ThumbnailSizes, []int{256}) {
		t.Fatalf("Sharing.ThumbnailSizes = %v, want unchanged", cfg.Sharing.ThumbnailSizes)
	}
	if cfg.Sharing.ThumbnailDefaultSize != 256 {
		t.Fatalf("Sharing.ThumbnailDefaultSize = %d, want unchanged", cfg.Sharing.ThumbnailDefaultSize)
	}
	if !cfg.Sharing.ThumbnailsGenerateForVideo {
		t.Fatal("Sharing.ThumbnailsGenerateForVideo = false, want unchanged")
	}
	if cfg.Sharing.VideoInlineMaxSizeMB != 25 {
		t.Fatalf("Sharing.VideoInlineMaxSizeMB = %d, want unchanged", cfg.Sharing.VideoInlineMaxSizeMB)
	}
	if !cfg.Sharing.VideoPlaybackEnabled {
		t.Fatal("Sharing.VideoPlaybackEnabled = false, want unchanged")
	}
	if len(logs) != 5 {
		t.Fatalf("logged %d invalid settings, want 5", len(logs))
	}
	for _, entry := range logs {
		if !strings.Contains(entry, "ignore invalid database setting") {
			t.Fatalf("log entry = %q, want invalid setting message", entry)
		}
	}
}

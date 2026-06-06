package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaultsWithoutConfigFile(t *testing.T) {
	t.Setenv("CONFIG_FILE", "")
	t.Setenv("APP_NODE_ID", "")
	t.Setenv("APP_COORDINATOR", "")
	t.Setenv("APP_STORAGE", "")
	t.Setenv("APP_COORDINATOR_URL", "")
	t.Setenv("APP_NODE_ADDRESS", "")
	t.Setenv("APP_API_REQUEST_TIMEOUT", "")
	t.Setenv("APP_FILE_TRANSFER_TIMEOUT", "")
	t.Setenv("AUTH_JWT_SECRET", "")
	t.Setenv("AUTH_NODE_SECRET", "")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("DB_DRIVER", "")
	t.Setenv("DB_DSN", "")
	t.Setenv("DB_AUTO_MIGRATE", "")
	t.Setenv("SEED_ADMIN_NAME", "")
	t.Setenv("SEED_ADMIN_PASSWORD", "")

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

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Database.Driver != "sqlite" {
		t.Fatalf("Database.Driver = %q, want %q", cfg.Database.Driver, "sqlite")
	}
	if cfg.Database.DSN != "dropoutbox.db" {
		t.Fatalf("Database.DSN = %q, want %q", cfg.Database.DSN, "dropoutbox.db")
	}
	if !cfg.Database.AutoMigrate {
		t.Fatal("Database.AutoMigrate = false, want true")
	}
	if cfg.App.APIRequestTimeout != 15*time.Second {
		t.Fatalf("App.APIRequestTimeout = %s, want %s", cfg.App.APIRequestTimeout, 15*time.Second)
	}
	if cfg.App.FileTransferTimeout != 30*time.Minute {
		t.Fatalf("App.FileTransferTimeout = %s, want %s", cfg.App.FileTransferTimeout, 30*time.Minute)
	}
}

func TestLoadYAMLConfigWithEnvOverride(t *testing.T) {
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
http:
  address: ":9090"
database:
  driver: postgres
  dsn: "host=db user=postgres password=postgres dbname=dropoutbox port=5432 sslmode=disable"
  auto_migrate: false
seed:
  admin_name: root
  admin_password: secret
`
	if err := os.WriteFile(filepath.Join(wd, "config.yaml"), []byte(configBody), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("HTTP_ADDR", ":8088")
	t.Setenv("DB_AUTO_MIGRATE", "true")
	t.Setenv("APP_API_REQUEST_TIMEOUT", "25s")

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
}

func TestLoadFromExplicitTOMLConfigFile(t *testing.T) {
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
}

func TestLoadRejectsMissingExplicitConfigFile(t *testing.T) {
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "missing.yaml"))

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error")
	}
}

func TestLoadAllowsMinimalStorageOnlyMode(t *testing.T) {
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

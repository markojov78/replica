package service

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"replica/internal/config"
	"replica/internal/db"
	"replica/internal/model"
	"replica/internal/repository"

	"gorm.io/gorm"
)

func TestConfigServiceUpdatePersistsOverridesAndCreatesRefreshCommands(t *testing.T) {
	database := openConfigServiceTestDB(t)
	for _, node := range []model.Node{
		{ID: "online", Status: model.NodeStatusOnline, Secret: "secret"},
		{ID: "offline", Status: model.NodeStatusOffline, Secret: "secret"},
		{ID: "unreachable", Status: model.NodeStatusUnreachable, Secret: "secret"},
		{ID: "disabled", Status: model.NodeStatusDisabled, Secret: "secret"},
		{ID: "revoked", Status: model.NodeStatusRevoked, Secret: "secret"},
	} {
		if err := database.Create(&node).Error; err != nil {
			t.Fatalf("Create(node %s) error = %v", node.ID, err)
		}
	}

	svc := newConfigServiceForTest(database)
	result, err := svc.Update([]ConfigUpdateItem{
		{Key: config.SettingSharingThumbnailSizes, Value: json.RawMessage(`[128,512]`)},
		{Key: config.SettingSharingThumbnailDefaultSize, Value: json.RawMessage(`512`)},
		{Key: config.SettingSharingVideoInlineMaxSizeMB, Value: json.RawMessage(`50`)},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got := configItemValue[int](t, result.Items, config.SettingSharingVideoInlineMaxSizeMB); got != 50 {
		t.Fatalf("video inline max size = %d, want 50", got)
	}

	var setting model.Setting
	if err := database.First(&setting, "key = ?", config.SettingSharingThumbnailSizes).Error; err != nil {
		t.Fatalf("First(setting) error = %v", err)
	}
	if setting.Value != `[128,512]` {
		t.Fatalf("thumbnail sizes setting = %q, want [128,512]", setting.Value)
	}

	var commands []model.Command
	if err := database.Order("node_id asc").Find(&commands).Error; err != nil {
		t.Fatalf("Find(commands) error = %v", err)
	}
	if len(commands) != 3 {
		t.Fatalf("commands count = %d, want 3", len(commands))
	}
	for _, command := range commands {
		if command.Type != model.NodeCommandTypeRefreshConfig || command.Status != model.NodeCommandStatusPending {
			t.Fatalf("command = %+v, want pending refresh_config", command)
		}
		if string(command.Payload) != "{}" {
			t.Fatalf("command.Payload = %s, want {}", command.Payload)
		}
		switch command.NodeID {
		case "offline", "online", "unreachable":
		default:
			t.Fatalf("command.NodeID = %q, want online/offline/unreachable only", command.NodeID)
		}
	}
}

func TestConfigServiceRejectsInvalidUpdates(t *testing.T) {
	database := openConfigServiceTestDB(t)
	svc := newConfigServiceForTest(database)

	tests := []struct {
		name  string
		items []ConfigUpdateItem
		want  error
	}{
		{name: "empty", items: nil, want: ErrEmptyConfigUpdate},
		{name: "unknown key", items: []ConfigUpdateItem{{Key: "database.dsn", Value: json.RawMessage(`"x"`)}}, want: ErrUnknownConfigKey},
		{name: "duplicate sizes", items: []ConfigUpdateItem{{Key: config.SettingSharingThumbnailSizes, Value: json.RawMessage(`[128,128]`)}}, want: ErrInvalidConfigValue},
		{name: "default missing from sizes", items: []ConfigUpdateItem{
			{Key: config.SettingSharingThumbnailSizes, Value: json.RawMessage(`[128,512]`)},
			{Key: config.SettingSharingThumbnailDefaultSize, Value: json.RawMessage(`256`)},
		}, want: ErrInvalidConfigValue},
		{name: "wrong boolean type", items: []ConfigUpdateItem{{Key: config.SettingSharingVideoPlaybackEnabled, Value: json.RawMessage(`"true"`)}}, want: ErrInvalidConfigValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Update(tt.items)
			if err == nil {
				t.Fatal("Update() error = nil")
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("Update() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestConfigServiceDeleteFallsBackToBaseConfig(t *testing.T) {
	database := openConfigServiceTestDB(t)
	svc := newConfigServiceForTest(database)
	if _, err := svc.Update([]ConfigUpdateItem{{Key: config.SettingSharingVideoInlineMaxSizeMB, Value: json.RawMessage(`50`)}}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if err := svc.DeleteKey(config.SettingSharingVideoInlineMaxSizeMB); err != nil {
		t.Fatalf("DeleteKey() error = %v", err)
	}
	list, err := svc.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got := configItemValue[int](t, list.Items, config.SettingSharingVideoInlineMaxSizeMB); got != 25 {
		t.Fatalf("video inline max size after delete = %d, want 25", got)
	}
}

func openConfigServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	database, err := db.Open(config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "config-service.db")})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}
	return database
}

func newConfigServiceForTest(database *gorm.DB) *ConfigService {
	return NewConfigService(repository.NewConfigRepository(database), config.Config{
		Sharing: config.SharingConfig{
			ThumbnailSizes:             []int{128, 256, 512},
			ThumbnailDefaultSize:       256,
			ThumbnailsGenerateForVideo: true,
			VideoInlineMaxSizeMB:       25,
			VideoPlaybackEnabled:       true,
		},
	})
}

func configItemValue[T any](t *testing.T, items []ConfigItem, key string) T {
	t.Helper()
	for _, item := range items {
		if item.Key == key {
			value, ok := item.Value.(T)
			if !ok {
				t.Fatalf("item %s has type %T", key, item.Value)
			}
			return value
		}
	}
	t.Fatalf("missing item %s", key)
	var zero T
	return zero
}

package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"replica/internal/config"
	"replica/internal/model"
	"replica/internal/repository"
)

var (
	ErrEmptyConfigUpdate    = errors.New("empty config update")
	ErrUnknownConfigKey     = errors.New("unknown configuration key")
	ErrInvalidConfigValue   = errors.New("invalid configuration value")
	ErrInvalidConfigSetting = errors.New("invalid configuration setting")
)

var configKeys = []string{
	config.SettingSharingThumbnailSizes,
	config.SettingSharingThumbnailDefaultSize,
	config.SettingSharingThumbnailsGenerateForVideo,
	config.SettingSharingVideoInlineMaxSizeMB,
	config.SettingSharingVideoPlaybackEnabled,
}

const legacySettingSharingThumbnailSizes = "sharing.thumbnails.sizes"

var configCommandNodeStatuses = []model.NodeStatus{
	model.NodeStatusOffline,
	model.NodeStatusUnreachable,
	model.NodeStatusOnline,
}

type ConfigService struct {
	configs *repository.ConfigRepository
	base    config.Config

	mu        sync.RWMutex
	effective config.Config
}

type ConfigList struct {
	Items []ConfigItem `json:"items"`
}

type ConfigItem struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

type ConfigUpdateItem struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

func NewConfigService(configs *repository.ConfigRepository, base config.Config) *ConfigService {
	return &ConfigService{
		configs:   configs,
		base:      base,
		effective: base,
	}
}

func (s *ConfigService) Load(logf ...func(string, ...any)) error {
	if err := s.configs.RenameSettingKey(legacySettingSharingThumbnailSizes, config.SettingSharingThumbnailSizes); err != nil {
		return err
	}
	settings, err := s.configs.FindSettings(configKeys...)
	if err != nil {
		return err
	}

	var logger func(string, ...any)
	if len(logf) > 0 {
		logger = logf[0]
	}
	effective := s.base
	effective.ApplyDatabaseSettings(settingValues(settings), logger)
	s.mu.Lock()
	s.effective = effective
	s.mu.Unlock()
	return nil
}

func (s *ConfigService) EffectiveConfig() config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.effective
}

func (s *ConfigService) List() (*ConfigList, error) {
	s.mu.RLock()
	effective := s.effective
	s.mu.RUnlock()

	return &ConfigList{Items: configItems(effective)}, nil
}

func (s *ConfigService) Update(items []ConfigUpdateItem) (*ConfigList, error) {
	if len(items) == 0 {
		return nil, ErrEmptyConfigUpdate
	}

	settings, err := s.configs.FindSettings(configKeys...)
	if err != nil {
		return nil, err
	}
	values := settingValues(settings)
	for _, item := range items {
		key, value, err := validateConfigUpdateItem(item)
		if err != nil {
			return nil, err
		}
		values[key] = value
	}

	effective, err := s.effectiveFromSettings(values)
	if err != nil {
		return nil, err
	}
	if err := validateEffectiveSharingConfig(effective); err != nil {
		return nil, err
	}

	updates := make(map[string]string, len(items))
	for _, item := range items {
		key, value, err := validateConfigUpdateItem(item)
		if err != nil {
			return nil, err
		}
		updates[key] = value
	}

	if err := s.configs.UpdateSettings(updates, configCommandNodeStatuses); err != nil {
		return nil, err
	}
	s.setEffective(effective)
	return &ConfigList{Items: configItems(effective)}, nil
}

func (s *ConfigService) DeleteAll() error {
	if err := s.configs.DeleteSettings(configKeys, configCommandNodeStatuses); err != nil {
		return err
	}
	s.setEffective(s.base)
	return nil
}

func (s *ConfigService) DeleteKey(key string) error {
	if !knownConfigKey(key) {
		return ErrUnknownConfigKey
	}

	settings, err := s.configs.FindSettings(configKeys...)
	if err != nil {
		return err
	}
	values := settingValues(settings)
	delete(values, key)
	effective, err := s.effectiveFromSettings(values)
	if err != nil {
		return err
	}
	if err := validateEffectiveSharingConfig(effective); err != nil {
		return err
	}

	if err := s.configs.DeleteSettings([]string{key}, configCommandNodeStatuses); err != nil {
		return err
	}
	s.setEffective(effective)
	return nil
}

func (s *ConfigService) effectiveFromSettings(values map[string]string) (config.Config, error) {
	effective := s.base
	for key, value := range values {
		if !knownConfigKey(key) {
			continue
		}
		if err := applySettingValue(&effective, key, value); err != nil {
			return config.Config{}, err
		}
	}
	return effective, nil
}

func (s *ConfigService) setEffective(effective config.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.effective = effective
}

func configItems(cfg config.Config) []ConfigItem {
	return []ConfigItem{
		{Key: config.SettingSharingThumbnailSizes, Value: append([]int(nil), cfg.Sharing.ThumbnailSizes...)},
		{Key: config.SettingSharingThumbnailDefaultSize, Value: cfg.Sharing.ThumbnailDefaultSize},
		{Key: config.SettingSharingThumbnailsGenerateForVideo, Value: cfg.Sharing.ThumbnailsGenerateForVideo},
		{Key: config.SettingSharingVideoInlineMaxSizeMB, Value: cfg.Sharing.VideoInlineMaxSizeMB},
		{Key: config.SettingSharingVideoPlaybackEnabled, Value: cfg.Sharing.VideoPlaybackEnabled},
	}
}

func validateConfigUpdateItem(item ConfigUpdateItem) (string, string, error) {
	if !knownConfigKey(item.Key) {
		return "", "", ErrUnknownConfigKey
	}

	switch item.Key {
	case config.SettingSharingThumbnailSizes:
		var values []int
		if err := json.Unmarshal(item.Value, &values); err != nil {
			return "", "", ErrInvalidConfigValue
		}
		if err := validateThumbnailSizes(values); err != nil {
			return "", "", err
		}
		data, err := json.Marshal(values)
		if err != nil {
			return "", "", err
		}
		return item.Key, string(data), nil
	case config.SettingSharingThumbnailDefaultSize, config.SettingSharingVideoInlineMaxSizeMB:
		var value int
		if err := json.Unmarshal(item.Value, &value); err != nil {
			return "", "", ErrInvalidConfigValue
		}
		if value <= 0 {
			return "", "", ErrInvalidConfigValue
		}
		return item.Key, strconv.Itoa(value), nil
	case config.SettingSharingThumbnailsGenerateForVideo, config.SettingSharingVideoPlaybackEnabled:
		var value bool
		if err := json.Unmarshal(item.Value, &value); err != nil {
			return "", "", ErrInvalidConfigValue
		}
		return item.Key, strconv.FormatBool(value), nil
	default:
		return "", "", ErrUnknownConfigKey
	}
}

func applySettingValue(cfg *config.Config, key string, value string) error {
	switch key {
	case config.SettingSharingThumbnailSizes:
		var sizes []int
		if err := json.Unmarshal([]byte(value), &sizes); err != nil {
			return fmt.Errorf("%w: %s", ErrInvalidConfigSetting, key)
		}
		if err := validateThumbnailSizes(sizes); err != nil {
			return err
		}
		cfg.Sharing.ThumbnailSizes = sizes
	case config.SettingSharingThumbnailDefaultSize:
		value, err := strconv.Atoi(value)
		if err != nil || value <= 0 {
			return ErrInvalidConfigSetting
		}
		cfg.Sharing.ThumbnailDefaultSize = value
	case config.SettingSharingThumbnailsGenerateForVideo:
		value, err := strconv.ParseBool(value)
		if err != nil {
			return ErrInvalidConfigSetting
		}
		cfg.Sharing.ThumbnailsGenerateForVideo = value
	case config.SettingSharingVideoInlineMaxSizeMB:
		value, err := strconv.Atoi(value)
		if err != nil || value <= 0 {
			return ErrInvalidConfigSetting
		}
		cfg.Sharing.VideoInlineMaxSizeMB = value
	case config.SettingSharingVideoPlaybackEnabled:
		value, err := strconv.ParseBool(value)
		if err != nil {
			return ErrInvalidConfigSetting
		}
		cfg.Sharing.VideoPlaybackEnabled = value
	}
	return nil
}

func validateEffectiveSharingConfig(cfg config.Config) error {
	if err := validateThumbnailSizes(cfg.Sharing.ThumbnailSizes); err != nil {
		return err
	}
	if cfg.Sharing.ThumbnailDefaultSize <= 0 {
		return ErrInvalidConfigValue
	}
	if !slices.Contains(cfg.Sharing.ThumbnailSizes, cfg.Sharing.ThumbnailDefaultSize) {
		return ErrInvalidConfigValue
	}
	if cfg.Sharing.VideoInlineMaxSizeMB <= 0 {
		return ErrInvalidConfigValue
	}
	return nil
}

func validateThumbnailSizes(values []int) error {
	if len(values) == 0 {
		return ErrInvalidConfigValue
	}
	seen := make(map[int]struct{}, len(values))
	for _, value := range values {
		if value <= 0 {
			return ErrInvalidConfigValue
		}
		if _, ok := seen[value]; ok {
			return ErrInvalidConfigValue
		}
		seen[value] = struct{}{}
	}
	return nil
}

func knownConfigKey(key string) bool {
	return slices.Contains(configKeys, key)
}

func settingValues(settings map[string]model.Setting) map[string]string {
	values := make(map[string]string, len(settings))
	for key, setting := range settings {
		values[key] = setting.Value
	}
	return values
}

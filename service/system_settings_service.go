package service

import (
	"fmt"
	"sync"

	"dinq_message/model"

	"gorm.io/gorm"
)

// SystemSettingsService 系统配置服务
type SystemSettingsService struct {
	db              *gorm.DB
	settingsCache   map[string]string
	settingsCacheMu sync.RWMutex
}

func NewSystemSettingsService(db *gorm.DB) *SystemSettingsService {
	service := &SystemSettingsService{
		db:            db,
		settingsCache: make(map[string]string),
	}
	// 启动时加载所有配置到缓存
	service.LoadSettings()
	return service
}

// LoadSettings 从数据库加载所有配置到内存缓存
func (s *SystemSettingsService) LoadSettings() error {
	var settings []model.SystemSettings
	if err := s.db.Find(&settings).Error; err != nil {
		return fmt.Errorf("failed to load system settings: %w", err)
	}

	s.settingsCacheMu.Lock()
	defer s.settingsCacheMu.Unlock()

	for _, setting := range settings {
		s.settingsCache[setting.SettingKey] = setting.SettingValue
	}

	return nil
}

// GetSetting 获取配置值（从缓存）
func (s *SystemSettingsService) GetSetting(key string) (string, bool) {
	s.settingsCacheMu.RLock()
	defer s.settingsCacheMu.RUnlock()

	value, exists := s.settingsCache[key]
	return value, exists
}

// GetBoolSetting 获取布尔类型配置
func (s *SystemSettingsService) GetBoolSetting(key string, defaultValue bool) bool {
	value, exists := s.GetSetting(key)
	if !exists {
		return defaultValue
	}
	return value == "true"
}

// IsFeatureEnabled 检查功能是否启用
func (s *SystemSettingsService) IsFeatureEnabled(featureKey string) bool {
	return s.GetBoolSetting(featureKey, false)
}

// UpdateSetting 更新配置（同时更新数据库和缓存）
func (s *SystemSettingsService) UpdateSetting(key, value string) error {
	// 更新数据库
	result := s.db.Model(&model.SystemSettings{}).
		Where("setting_key = ?", key).
		Update("setting_value", value)

	if result.Error != nil {
		return fmt.Errorf("failed to update setting: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("setting key not found: %s", key)
	}

	// 更新缓存
	s.settingsCacheMu.Lock()
	s.settingsCache[key] = value
	s.settingsCacheMu.Unlock()

	return nil
}

// GetAllSettings 获取所有配置
func (s *SystemSettingsService) GetAllSettings() (map[string]string, error) {
	s.settingsCacheMu.RLock()
	defer s.settingsCacheMu.RUnlock()

	// 返回缓存的副本
	result := make(map[string]string, len(s.settingsCache))
	for k, v := range s.settingsCache {
		result[k] = v
	}

	return result, nil
}

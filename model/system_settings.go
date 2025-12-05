package model

import (
	"time"

	"github.com/google/uuid"
)

// SystemSettings 系统配置（超管全局配置）
type SystemSettings struct {
	ID           uuid.UUID `json:"id" gorm:"type:uuid;primary_key;default:gen_random_uuid()"`
	SettingKey   string    `json:"setting_key" gorm:"unique;not null"`
	SettingValue string    `json:"setting_value" gorm:"not null"`
	Description  string    `json:"description"`
	UpdatedAt    time.Time `json:"updated_at" gorm:"default:now()"`
}

func (SystemSettings) TableName() string {
	return "system_settings"
}

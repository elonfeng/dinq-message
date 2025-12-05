package model

import (
	"time"

	"github.com/google/uuid"
)

// NotificationTemplate 通知模板表
type NotificationTemplate struct {
	ID              uuid.UUID `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Type            string    `json:"type" gorm:"type:varchar(50);not null;uniqueIndex"` // 'new_message' | 'new_group_message' | 'system' | 'card_completed' 等
	Title           string    `json:"title" gorm:"type:varchar(200);not null"`           // 标题模板，支持变量：{{sender_name}}, {{content}}
	ContentTemplate *string   `json:"content_template,omitempty" gorm:"type:text"`       // 内容模板
	Priority        int       `json:"priority" gorm:"default:0"`                         // 优先级：0-普通 1-重要 2-紧急
	EnablePush      bool      `json:"enable_push" gorm:"default:true"`                   // 是否启用推送
	EnableWebsocket bool      `json:"enable_websocket" gorm:"default:true"`              // 是否通过 WebSocket 推送
	IsActive        bool      `json:"is_active" gorm:"default:true"`                     // 是否启用此模板
	CreatedAt       time.Time `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt       time.Time `json:"updated_at" gorm:"autoUpdateTime"`
	Description     *string   `json:"description,omitempty" gorm:"type:text"` // 模板说明
}

func (NotificationTemplate) TableName() string {
	return "notification_templates"
}

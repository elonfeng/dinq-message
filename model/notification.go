package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Notification 通知表
type Notification struct {
	ID               uuid.UUID       `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID           uuid.UUID       `json:"user_id" gorm:"type:uuid;not null;index"`
	NotificationType string          `json:"notification_type" gorm:"type:varchar(30);not null"` // 'system' | 'message' | 'card_completed' | 'custom'
	Title            string          `json:"title" gorm:"type:varchar(200);not null"`
	Content          *string         `json:"content,omitempty" gorm:"type:text"`
	Metadata         json.RawMessage `json:"metadata,omitempty" gorm:"type:jsonb"` // JSONB 字段
	IsRead           bool            `json:"is_read" gorm:"default:false"`
	ReadAt           *time.Time      `json:"read_at,omitempty"`
	Priority         int             `json:"priority" gorm:"default:0"` // 0:普通 1:重要 2:紧急
	CreatedAt        time.Time       `json:"created_at" gorm:"autoCreateTime"`
	ExpiresAt        *time.Time      `json:"expires_at,omitempty"`
}

func (Notification) TableName() string {
	return "notifications"
}

// NotificationMetadata 通知元数据结构（用于解析 metadata 字段）
type NotificationMetadata struct {
	// 跳转链接
	LinkURL string `json:"link_url,omitempty"`

	// 关联对象 ID
	ConversationID *uuid.UUID `json:"conversation_id,omitempty"`
	MessageID      *uuid.UUID `json:"message_id,omitempty"`
	CardID         *uuid.UUID `json:"card_id,omitempty"`

	// 额外信息
	SenderID   *uuid.UUID `json:"sender_id,omitempty"`
	SenderName string     `json:"sender_name,omitempty"`

	// 操作按钮（预留）
	Actions []NotificationAction `json:"actions,omitempty"`
}

// NotificationAction 通知操作按钮
type NotificationAction struct {
	Label  string `json:"label"`
	Action string `json:"action"` // 'open_conversation' | 'view_card' | 'dismiss'
	URL    string `json:"url,omitempty"`
}

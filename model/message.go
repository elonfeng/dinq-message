package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Message 消息表
type Message struct {
	ID               uuid.UUID       `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ConversationID   uuid.UUID       `json:"conversation_id" gorm:"type:uuid;not null;index"`
	SenderID         uuid.UUID       `json:"sender_id" gorm:"type:uuid;not null;index"`
	MessageType      string          `json:"message_type" gorm:"type:varchar(20);not null"` // 'text' | 'image' | 'video' | 'emoji'
	Content          *string         `json:"content,omitempty" gorm:"type:text"`
	Metadata         json.RawMessage `json:"metadata,omitempty" gorm:"type:jsonb"`        // JSONB 字段
	Status           string          `json:"status" gorm:"type:varchar(20);default:sent"` // 'sent' | 'delivered' | 'read'
	ReplyToMessageID *uuid.UUID      `json:"reply_to_message_id,omitempty" gorm:"type:uuid"`
	IsRecalled       bool            `json:"is_recalled" gorm:"default:false"`
	RecalledAt       *time.Time      `json:"recalled_at,omitempty"`
	CreatedAt        time.Time       `json:"created_at" gorm:"autoCreateTime"`
}

func (Message) TableName() string {
	return "messages"
}

// MessageMetadata 消息元数据结构（用于解析 metadata 字段）
type MessageMetadata struct {
	// 图片相关
	ImageURL     string `json:"image_url,omitempty"`
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`

	// 视频相关
	VideoURL string `json:"video_url,omitempty"`
	Duration int    `json:"duration,omitempty"`  // 视频时长（秒）
	CoverURL string `json:"cover_url,omitempty"` // 视频封面

	// 表情相关
	EmojiID   string `json:"emoji_id,omitempty"`
	EmojiName string `json:"emoji_name,omitempty"`

	// 回复消息预览
	ReplyToContent string `json:"reply_to_content,omitempty"`
}

// MessageWithSender 消息详情（包含发送者信息）
type MessageWithSender struct {
	Message
	SenderName   string  `json:"sender_name"`
	SenderAvatar *string `json:"sender_avatar,omitempty"`
}

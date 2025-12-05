package model

import (
	"time"

	"github.com/google/uuid"
)

// Conversation 会话表
type Conversation struct {
	ID               uuid.UUID  `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ConversationType string     `json:"conversation_type" gorm:"type:varchar(20);not null"` // 'private' | 'group'
	GroupName        *string    `json:"group_name,omitempty" gorm:"type:varchar(100)"`
	CreatedAt        time.Time  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt        time.Time  `json:"updated_at" gorm:"autoUpdateTime"`
	LastMessageAt    *time.Time `json:"last_message_at,omitempty"`
	LastMessageID    *uuid.UUID `json:"last_message_id,omitempty" gorm:"type:uuid"`
}

func (Conversation) TableName() string {
	return "conversations"
}

// ConversationMember 会话成员表
type ConversationMember struct {
	ID                uuid.UUID  `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ConversationID    uuid.UUID  `json:"conversation_id" gorm:"type:uuid;not null;index"`
	UserID            uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index"`
	Role              string     `json:"role" gorm:"type:varchar(20);default:member"` // 'owner' | 'admin' | 'member'
	IsMuted           bool       `json:"is_muted" gorm:"default:false"`
	IsHidden          bool       `json:"is_hidden" gorm:"default:false"` // 软删除标记,收到新消息时自动恢复
	JoinedAt          time.Time  `json:"joined_at" gorm:"autoCreateTime"`
	LeftAt            *time.Time `json:"left_at,omitempty"`
	UnreadCount       int        `json:"unread_count" gorm:"default:0"`
	LastReadMessageID *uuid.UUID `json:"last_read_message_id,omitempty" gorm:"type:uuid"`
	LastReadAt        *time.Time `json:"last_read_at,omitempty"`

	// 用户信息（从agent查询补充，不存数据库）
	Name      *string `json:"name,omitempty" gorm:"-"`
	AvatarURL *string `json:"avatar_url,omitempty" gorm:"-"`
	Username  *string `json:"username,omitempty" gorm:"-"`
	Position  *string `json:"position,omitempty" gorm:"-"`
	Company   *string `json:"company,omitempty" gorm:"-"`
}

func (ConversationMember) TableName() string {
	return "conversation_members"
}

// ConversationWithMembers 会话详情（包含成员信息）
type ConversationWithMembers struct {
	Conversation
	Members []ConversationMember `json:"members"`
}

// ConversationListItem 会话列表项(包含扩展信息)
type ConversationListItem struct {
	Conversation
	UnreadCount     int                  `json:"unread_count"`      // 未读消息数量
	LastMessageTime *time.Time           `json:"last_message_time"` // 最新消息时间
	LastMessageText *string              `json:"last_message_text"` // 最新消息内容预览
	OnlineStatus    map[string]bool      `json:"online_status"`     // 成员在线状态 map[userID]isOnline
	Members         []ConversationMember `json:"members"`           // 会话成员
}

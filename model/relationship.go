package model

import (
	"time"

	"github.com/google/uuid"
)

// UserRelationship 用户关系表
type UserRelationship struct {
	ID               uuid.UUID `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID           uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
	TargetUserID     uuid.UUID `json:"target_user_id" gorm:"type:uuid;not null;index"`
	RelationshipType string    `json:"relationship_type" gorm:"type:varchar(20);not null"` // 'blocked' | 'friend' | 'follow' | 'muted'
	CreatedAt        time.Time `json:"created_at" gorm:"autoCreateTime"`
}

func (UserRelationship) TableName() string {
	return "user_relationships"
}

package service

import (
	"fmt"

	"dinq_message/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type RelationshipService struct {
	db *gorm.DB
}

func NewRelationshipService(db *gorm.DB) *RelationshipService {
	return &RelationshipService{db: db}
}

// BlockUser 拉黑用户
func (s *RelationshipService) BlockUser(userID, targetUserID uuid.UUID) error {
	// 检查是否已经拉黑
	var count int64
	err := s.db.Model(&model.UserRelationship{}).
		Where("user_id = ? AND target_user_id = ? AND relationship_type = ?", userID, targetUserID, "blocked").
		Count(&count).Error

	if err != nil {
		return fmt.Errorf("failed to check relationship: %w", err)
	}

	if count > 0 {
		return fmt.Errorf("user already blocked")
	}

	// 创建拉黑关系
	relationship := &model.UserRelationship{
		UserID:           userID,
		TargetUserID:     targetUserID,
		RelationshipType: "blocked",
	}

	if err := s.db.Create(relationship).Error; err != nil {
		return fmt.Errorf("failed to block user: %w", err)
	}

	return nil
}

// UnblockUser 取消拉黑
func (s *RelationshipService) UnblockUser(userID, targetUserID uuid.UUID) error {
	result := s.db.Where("user_id = ? AND target_user_id = ? AND relationship_type = ?", userID, targetUserID, "blocked").
		Delete(&model.UserRelationship{})

	if result.Error != nil {
		return fmt.Errorf("failed to unblock user: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("user not blocked")
	}

	return nil
}

// GetBlockedUsers 获取拉黑列表
func (s *RelationshipService) GetBlockedUsers(userID uuid.UUID) ([]model.UserRelationship, error) {
	var relationships []model.UserRelationship
	err := s.db.Where("user_id = ? AND relationship_type = ?", userID, "blocked").
		Order("created_at DESC").
		Find(&relationships).Error

	if err != nil {
		return nil, fmt.Errorf("failed to query blocked users: %w", err)
	}

	return relationships, nil
}

// IsBlocked 检查是否被拉黑
func (s *RelationshipService) IsBlocked(userID, targetUserID uuid.UUID) (bool, error) {
	var count int64
	err := s.db.Model(&model.UserRelationship{}).
		Where("user_id = ? AND target_user_id = ? AND relationship_type = ?", userID, targetUserID, "blocked").
		Count(&count).Error

	return count > 0, err
}

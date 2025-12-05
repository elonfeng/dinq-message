package service

import (
	"fmt"
	"strings"

	"dinq_message/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type NotificationTemplateService struct {
	db *gorm.DB
}

func NewNotificationTemplateService(db *gorm.DB) *NotificationTemplateService {
	return &NotificationTemplateService{db: db}
}

// GetTemplate 获取通知模板
func (s *NotificationTemplateService) GetTemplate(notifType string) (*model.NotificationTemplate, error) {
	var template model.NotificationTemplate
	err := s.db.Where("type = ? AND is_active = ?", notifType, true).First(&template).Error
	if err != nil {
		return nil, fmt.Errorf("template not found: %w", err)
	}
	return &template, nil
}

// RenderTemplate 渲染模板，替换变量
func (s *NotificationTemplateService) RenderTemplate(template string, vars map[string]string) string {
	result := template
	for key, value := range vars {
		placeholder := "{{" + key + "}}"
		result = strings.ReplaceAll(result, placeholder, value)
	}
	return result
}

// CreateTemplate 创建通知模板
func (s *NotificationTemplateService) CreateTemplate(req *model.NotificationTemplate) (*model.NotificationTemplate, error) {
	if err := s.db.Create(req).Error; err != nil {
		return nil, fmt.Errorf("failed to create template: %w", err)
	}
	return req, nil
}

// UpdateTemplate 更新通知模板
func (s *NotificationTemplateService) UpdateTemplate(id uuid.UUID, updates map[string]interface{}) error {
	result := s.db.Model(&model.NotificationTemplate{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("failed to update template: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("template not found")
	}
	return nil
}

// ListTemplates 获取所有模板列表
func (s *NotificationTemplateService) ListTemplates() ([]model.NotificationTemplate, error) {
	var templates []model.NotificationTemplate
	err := s.db.Order("type ASC").Find(&templates).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list templates: %w", err)
	}
	return templates, nil
}

// DeleteTemplate 删除模板
func (s *NotificationTemplateService) DeleteTemplate(id uuid.UUID) error {
	result := s.db.Delete(&model.NotificationTemplate{}, "id = ?", id)
	if result.Error != nil {
		return fmt.Errorf("failed to delete template: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("template not found")
	}
	return nil
}

// InitDefaultTemplates 初始化默认通知模板
func (s *NotificationTemplateService) InitDefaultTemplates() error {
	defaultTemplates := []model.NotificationTemplate{
		{
			Type:            "new_message",
			Title:           "New Message",
			ContentTemplate: stringPtr("{{sender_name}}: {{content}}"),
			Priority:        0,
			EnablePush:      true,
			EnableWebsocket: true,
			IsActive:        true,
			Description:     stringPtr("私信新消息通知"),
		},
		{
			Type:            "new_group_message",
			Title:           "New Group Message",
			ContentTemplate: stringPtr("{{sender_name}} in {{group_name}}: {{content}}"),
			Priority:        0,
			EnablePush:      true,
			EnableWebsocket: true,
			IsActive:        true,
			Description:     stringPtr("群聊新消息通知"),
		},
		{
			Type:            "system",
			Title:           "System Notification",
			ContentTemplate: stringPtr("{{content}}"),
			Priority:        1,
			EnablePush:      true,
			EnableWebsocket: true,
			IsActive:        true,
			Description:     stringPtr("系统通知"),
		},
		{
			Type:            "card_completed",
			Title:           "Card Completed",
			ContentTemplate: stringPtr("Your card {{card_name}} is ready!"),
			Priority:        0,
			EnablePush:      true,
			EnableWebsocket: true,
			IsActive:        true,
			Description:     stringPtr("卡片生成完成通知"),
		},
	}

	for _, template := range defaultTemplates {
		// 检查是否已存在
		var existing model.NotificationTemplate
		err := s.db.Where("type = ?", template.Type).First(&existing).Error
		if err == gorm.ErrRecordNotFound {
			// 不存在，创建
			if err := s.db.Create(&template).Error; err != nil {
				return fmt.Errorf("failed to create default template %s: %w", template.Type, err)
			}
		}
	}

	return nil
}

func stringPtr(s string) *string {
	return &s
}

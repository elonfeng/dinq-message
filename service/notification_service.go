package service

import (
	"encoding/json"
	"fmt"
	"time"

	"dinq_message/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type NotificationService struct {
	db          *gorm.DB
	templateSvc *NotificationTemplateService
	hubNotifier HubNotifier // Interface to send WebSocket notifications
}

// HubNotifier 接口用于发送WebSocket通知
type HubNotifier interface {
	SendNotification(userID uuid.UUID, notification interface{}) bool
	IsUserOnline(userID uuid.UUID) bool
}

func NewNotificationService(db *gorm.DB) *NotificationService {
	return &NotificationService{
		db:          db,
		templateSvc: NewNotificationTemplateService(db),
	}
}

// SetHubNotifier 设置Hub通知器（用于依赖注入）
func (s *NotificationService) SetHubNotifier(notifier HubNotifier) {
	s.hubNotifier = notifier
}

// CreateNotification 创建通知
func (s *NotificationService) CreateNotification(userID uuid.UUID, notifType, title string, content *string, metadata map[string]interface{}, priority int, expiresAt *time.Time) (*model.Notification, error) {
	notification := &model.Notification{
		UserID:           userID,
		NotificationType: notifType,
		Title:            title,
		Content:          content,
		IsRead:           false,
		Priority:         priority,
		ExpiresAt:        expiresAt,
	}

	// 序列化 metadata
	if metadata != nil {
		metadataBytes, err := json.Marshal(metadata)
		if err != nil {
			return nil, fmt.Errorf("invalid metadata: %w", err)
		}
		notification.Metadata = metadataBytes
	}

	// 保存到数据库
	if err := s.db.Create(notification).Error; err != nil {
		return nil, fmt.Errorf("failed to create notification: %w", err)
	}

	// 只推送给在线用户
	if s.hubNotifier != nil && s.hubNotifier.IsUserOnline(userID) {
		s.hubNotifier.SendNotification(userID, notification)
	}

	return notification, nil
}

// CreateNotificationWithTemplate 使用模板创建通知
func (s *NotificationService) CreateNotificationWithTemplate(userID uuid.UUID, notifType string, templateVars map[string]string, metadata map[string]interface{}) (*model.Notification, error) {
	// 获取模板
	template, err := s.templateSvc.GetTemplate(notifType)
	if err != nil {
		// 如果没有模板，使用默认值
		return s.CreateNotification(userID, notifType, "Notification", nil, metadata, 0, nil)
	}

	// 检查模板是否启用
	if !template.IsActive {
		return nil, fmt.Errorf("notification template is not active")
	}

	// 渲染标题和内容
	title := s.templateSvc.RenderTemplate(template.Title, templateVars)
	var content *string
	if template.ContentTemplate != nil {
		rendered := s.templateSvc.RenderTemplate(*template.ContentTemplate, templateVars)
		content = &rendered
	}

	// 创建通知
	notification := &model.Notification{
		UserID:           userID,
		NotificationType: notifType,
		Title:            title,
		Content:          content,
		IsRead:           false,
		Priority:         template.Priority,
		ExpiresAt:        nil,
	}

	// 序列化 metadata
	if metadata != nil {
		metadataBytes, err := json.Marshal(metadata)
		if err != nil {
			return nil, fmt.Errorf("invalid metadata: %w", err)
		}
		notification.Metadata = metadataBytes
	}

	// 保存到数据库
	if err := s.db.Create(notification).Error; err != nil {
		return nil, fmt.Errorf("failed to create notification: %w", err)
	}

	// 只推送给在线用户（如果模板启用WebSocket推送）
	if template.EnableWebsocket && s.hubNotifier != nil && s.hubNotifier.IsUserOnline(userID) {
		s.hubNotifier.SendNotification(userID, notification)
	}

	return notification, nil
}

// GetNotifications 获取用户的通知列表
func (s *NotificationService) GetNotifications(userID uuid.UUID, limit, offset int, unreadOnly bool) ([]model.Notification, error) {
	var notifications []model.Notification

	query := s.db.Where("user_id = ? AND (expires_at IS NULL OR expires_at > ?)", userID, time.Now())

	if unreadOnly {
		query = query.Where("is_read = ?", false)
	}

	err := query.Order("priority DESC, created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&notifications).Error

	if err != nil {
		return nil, fmt.Errorf("failed to query notifications: %w", err)
	}

	return notifications, nil
}

// MarkAsRead 标记通知为已读
// GetNotificationDetail 获取通知详情并标记为已读
func (s *NotificationService) GetNotificationDetail(userID, notificationID uuid.UUID) (*model.Notification, error) {
	var notification model.Notification
	if err := s.db.Where("id = ? AND user_id = ?", notificationID, userID).First(&notification).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("notification not found")
		}
		return nil, fmt.Errorf("failed to get notification: %w", err)
	}

	// 如果未读，标记为已读
	if !notification.IsRead {
		now := time.Now()
		notification.IsRead = true
		notification.ReadAt = &now

		if err := s.db.Model(&notification).Updates(map[string]interface{}{
			"is_read": true,
			"read_at": now,
		}).Error; err != nil {
			// 读取成功但标记失败，仍然返回通知内容
			return &notification, nil
		}
	}

	return &notification, nil
}

// MarkAllAsRead 标记所有通知为已读
func (s *NotificationService) MarkAllAsRead(userID uuid.UUID) error {
	now := time.Now()
	return s.db.Model(&model.Notification{}).
		Where("user_id = ? AND is_read = ?", userID, false).
		Updates(map[string]interface{}{
			"is_read": true,
			"read_at": now,
		}).Error
}

// GetUnreadCount 获取未读通知数量
func (s *NotificationService) GetUnreadCount(userID uuid.UUID) (int, error) {
	var count int64
	err := s.db.Model(&model.Notification{}).
		Where("user_id = ? AND is_read = ? AND (expires_at IS NULL OR expires_at > ?)", userID, false, time.Now()).
		Count(&count).Error

	return int(count), err
}

// GetLatestNotificationTime 获取最新通知时间
func (s *NotificationService) GetLatestNotificationTime(userID uuid.UUID) (*time.Time, error) {
	var latestTime *time.Time
	err := s.db.Model(&model.Notification{}).
		Select("MAX(created_at)").
		Where("user_id = ? AND (expires_at IS NULL OR expires_at > ?)", userID, time.Now()).
		Scan(&latestTime).Error

	return latestTime, err
}

// GetNotificationSummary 获取通知摘要(未读数量+最新通知时间)
func (s *NotificationService) GetNotificationSummary(userID uuid.UUID) (map[string]interface{}, error) {
	type Summary struct {
		UnreadCount     int64      `gorm:"column:unread_count"`
		LatestNotifTime *time.Time `gorm:"column:latest_notif_time"`
	}

	var summary Summary
	err := s.db.Model(&model.Notification{}).
		Select("COUNT(CASE WHEN is_read = false THEN 1 END) as unread_count, MAX(created_at) as latest_notif_time").
		Where("user_id = ? AND (expires_at IS NULL OR expires_at > ?)", userID, time.Now()).
		Scan(&summary).Error

	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"unread_count":      int(summary.UnreadCount),
		"latest_notif_time": summary.LatestNotifTime,
	}, nil
}

// GetLatestUnreadNotification 获取最新一条未读通知
func (s *NotificationService) GetLatestUnreadNotification(userID uuid.UUID) (*model.Notification, error) {
	var notification model.Notification
	err := s.db.Where("user_id = ? AND is_read = ? AND (expires_at IS NULL OR expires_at > ?)", userID, false, time.Now()).
		Order("created_at DESC").
		First(&notification).Error

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil // 没有未读通知，返回nil不报错
		}
		return nil, fmt.Errorf("failed to get latest unread notification: %w", err)
	}

	return &notification, nil
}

// DeleteNotification 删除通知
func (s *NotificationService) DeleteNotification(userID, notificationID uuid.UUID) error {
	result := s.db.Where("id = ? AND user_id = ?", notificationID, userID).
		Delete(&model.Notification{})

	if result.Error != nil {
		return fmt.Errorf("failed to delete notification: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("notification not found")
	}

	return nil
}

// BatchSendNotification 批量发送通知
// userIDs: 接收者ID列表，如果为空表示发送给所有用户
func (s *NotificationService) BatchSendNotification(userIDs []uuid.UUID, notifType, title string, content *string, metadata map[string]interface{}, priority int) (int, error) {
	// 如果没有指定用户，获取所有用户
	var targetUserIDs []uuid.UUID
	if len(userIDs) == 0 {
		// 从gateway数据库获取所有用户
		// 这里需要跨服务调用，暂时返回错误提示
		return 0, fmt.Errorf("sending to all users requires user list")
	} else {
		targetUserIDs = userIDs
	}

	// 序列化 metadata
	var metadataBytes []byte
	if metadata != nil {
		var err error
		metadataBytes, err = json.Marshal(metadata)
		if err != nil {
			return 0, fmt.Errorf("invalid metadata: %w", err)
		}
	}

	// 批量创建通知
	successCount := 0
	for _, userID := range targetUserIDs {
		notification := &model.Notification{
			UserID:           userID,
			NotificationType: notifType,
			Title:            title,
			Content:          content,
			IsRead:           false,
			Priority:         priority,
			Metadata:         metadataBytes,
		}

		// 保存到数据库
		if err := s.db.Create(notification).Error; err != nil {
			// 记录错误但继续处理其他用户
			continue
		}

		successCount++

		// 只推送给在线用户
		if s.hubNotifier != nil && s.hubNotifier.IsUserOnline(userID) {
			s.hubNotifier.SendNotification(userID, notification)
		}
	}

	return successCount, nil
}

// SendNotificationWithTemplate 使用模板批量发送通知（统一方法）
// userIDs: 接收者ID列表，如果为空表示发送给所有用户
// templateType: 模板类型（notification_type）
// templateVars: 模板变量，用于渲染模板中的 {{variable}} 占位符
// metadata: 额外元数据
func (s *NotificationService) SendNotificationWithTemplate(userIDs []uuid.UUID, templateType string, templateVars map[string]string, metadata map[string]interface{}) (int, error) {
	// 如果没有指定用户，返回错误
	var targetUserIDs []uuid.UUID
	if len(userIDs) == 0 {
		return 0, fmt.Errorf("sending to all users requires user list")
	} else {
		targetUserIDs = userIDs
	}

	// 获取模板
	template, err := s.templateSvc.GetTemplate(templateType)
	if err != nil {
		return 0, fmt.Errorf("template not found: %w", err)
	}

	// 检查模板是否启用
	if !template.IsActive {
		return 0, fmt.Errorf("notification template is not active")
	}

	// 渲染标题和内容
	title := s.templateSvc.RenderTemplate(template.Title, templateVars)
	var content *string
	if template.ContentTemplate != nil {
		rendered := s.templateSvc.RenderTemplate(*template.ContentTemplate, templateVars)
		content = &rendered
	}

	// 序列化 metadata
	var metadataBytes []byte
	if metadata != nil {
		metadataBytes, err = json.Marshal(metadata)
		if err != nil {
			return 0, fmt.Errorf("invalid metadata: %w", err)
		}
	}

	// 批量创建通知
	successCount := 0
	for _, userID := range targetUserIDs {
		notification := &model.Notification{
			UserID:           userID,
			NotificationType: templateType,
			Title:            title,
			Content:          content,
			IsRead:           false,
			Priority:         template.Priority,
			Metadata:         metadataBytes,
		}

		// 保存到数据库
		if err := s.db.Create(notification).Error; err != nil {
			// 记录错误但继续处理其他用户
			continue
		}

		successCount++

		// 只推送给在线用户（根据模板配置）
		if template.EnableWebsocket && s.hubNotifier != nil && s.hubNotifier.IsUserOnline(userID) {
			s.hubNotifier.SendNotification(userID, notification)
		}
	}

	return successCount, nil
}

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"dinq_message/model"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type MessageService struct {
	db             *gorm.DB
	rdb            *redis.Client
	sysSvc         *SystemSettingsService
	maxVideoSizeMB int
	notifSvc       *NotificationService
	hubChecker     OnlineChecker              // Interface to check if user is online
	unreadNotifier UnreadCountNotifier        // Interface to notify unread count changes
	convNotifier   ConversationUpdateNotifier // Interface to notify conversation updates
}

// OnlineChecker 接口用于检查用户是否在线
type OnlineChecker interface {
	IsOnline(userID uuid.UUID) bool
	IsUserInConversation(userID uuid.UUID, conversationID uuid.UUID) bool
}

// UnreadCountNotifier 接口用于推送未读数量变化
type UnreadCountNotifier interface {
	SendUnreadCountUpdate(userID uuid.UUID, conversationID uuid.UUID, unreadCount int) bool
}

// ConversationUpdateNotifier 接口用于推送会话更新
type ConversationUpdateNotifier interface {
	SendConversationUpdate(userID uuid.UUID, conversationID uuid.UUID, lastMessageTime *time.Time, lastMessageText *string, unreadCount int) bool
}

func NewMessageService(db *gorm.DB, rdb *redis.Client, sysSvc *SystemSettingsService) *MessageService {
	return &MessageService{
		db:             db,
		rdb:            rdb,
		sysSvc:         sysSvc,
		maxVideoSizeMB: 5, // 默认5MB
	}
}

func NewMessageServiceWithConfig(db *gorm.DB, rdb *redis.Client, sysSvc *SystemSettingsService, maxVideoSizeMB int) *MessageService {
	return &MessageService{
		db:             db,
		rdb:            rdb,
		sysSvc:         sysSvc,
		maxVideoSizeMB: maxVideoSizeMB,
	}
}

// SetNotificationService 设置通知服务（用于依赖注入）
func (s *MessageService) SetNotificationService(notifSvc *NotificationService) {
	s.notifSvc = notifSvc
}

// SetHubChecker 设置在线状态检查器（用于依赖注入）
func (s *MessageService) SetHubChecker(checker OnlineChecker) {
	s.hubChecker = checker
}

// SetUnreadNotifier 设置未读数量通知器（用于依赖注入）
func (s *MessageService) SetUnreadNotifier(notifier UnreadCountNotifier) {
	s.unreadNotifier = notifier
}

// SetConversationNotifier 设置会话更新通知器（用于依赖注入）
func (s *MessageService) SetConversationNotifier(notifier ConversationUpdateNotifier) {
	s.convNotifier = notifier
}

// GetDB 获取数据库连接（用于高级查询）
func (s *MessageService) GetDB() *gorm.DB {
	return s.db
}

// SendMessageRequest 发送消息请求
type SendMessageRequest struct {
	ConversationID   uuid.UUID              `json:"conversation_id"`
	ReceiverID       *uuid.UUID             `json:"receiver_id,omitempty"` // 私聊时必须,群聊时不需要
	MessageType      string                 `json:"message_type"`          // 'text' | 'image' | 'video' | 'emoji'
	Content          *string                `json:"content,omitempty"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"`
	ReplyToMessageID *uuid.UUID             `json:"reply_to_message_id,omitempty"`
}

// SendMessage 发送消息
func (s *MessageService) SendMessage(senderID uuid.UUID, req *SendMessageRequest) (*model.Message, error) {
	ctx := context.Background()

	// 0. 验证输入
	if req.MessageType == "text" && (req.Content == nil || *req.Content == "") {
		return nil, fmt.Errorf("content is required for text messages")
	}

	// 1. 如果没有 conversation_id,创建或查找私聊会话
	conversationID := req.ConversationID
	conversationJustCreated := false
	if conversationID == uuid.Nil && req.ReceiverID != nil {
		// 私聊：查找或创建会话
		var err error
		conversationID, conversationJustCreated, err = s.getOrCreatePrivateConversation(senderID, *req.ReceiverID)
		if err != nil {
			return nil, fmt.Errorf("failed to get conversation: %w", err)
		}
	}

	if conversationID == uuid.Nil {
		return nil, fmt.Errorf("conversation_id is required")
	}

	// 2. 检查用户是否是会话成员
	isMember, err := s.isConversationMember(conversationID, senderID)
	if err != nil || !isMember {
		return nil, fmt.Errorf("user is not a member of this conversation")
	}

	// 3. 检查是否被拉黑
	if req.ReceiverID != nil {
		isBlocked, err := s.isBlocked(senderID, *req.ReceiverID)
		if err != nil {
			return nil, err
		}
		if isBlocked {
			return nil, fmt.Errorf("you are blocked by this user")
		}
	}

	// 4. 检查视频文件大小限制
	if req.MessageType == "video" && req.Metadata != nil {
		if fileSize, ok := req.Metadata["file_size"].(float64); ok {
			fileSizeMB := fileSize / (1024 * 1024)
			if fileSizeMB > float64(s.maxVideoSizeMB) {
				return nil, fmt.Errorf("video file size exceeds limit: max %dMB, got %.2fMB", s.maxVideoSizeMB, fileSizeMB)
			}
		}
	}

	// 5. 对于需要检查首条消息限制的情况，使用分布式锁防止并发问题
	if !conversationJustCreated && s.sysSvc.IsFeatureEnabled("enable_first_message_limit") {
		// 使用 Redis 锁确保检查和插入的原子性
		lockKey := fmt.Sprintf("lock:send_msg:%s:%s", conversationID, senderID)
		lockAcquired := false
		for i := 0; i < 3; i++ {
			ok, err := s.rdb.SetNX(ctx, lockKey, "1", 2*time.Second).Result()
			if err == nil && ok {
				lockAcquired = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		if !lockAcquired {
			return nil, fmt.Errorf("failed to acquire lock, please try again")
		}
		defer s.rdb.Del(ctx, lockKey)

		// 在锁内进行检查
		if !s.CheckCanSend(senderID, conversationID) {
			return nil, fmt.Errorf("first message limit: wait for reply before sending more messages")
		}
	} else if !conversationJustCreated && !s.CheckCanSend(senderID, conversationID) {
		// 功能未启用但仍需检查的情况（向后兼容）
		return nil, fmt.Errorf("first message limit: wait for reply before sending more messages")
	}

	// 6. 创建消息对象
	message := &model.Message{
		ConversationID:   conversationID,
		SenderID:         senderID,
		MessageType:      req.MessageType,
		Content:          req.Content,
		Status:           "sent",
		ReplyToMessageID: req.ReplyToMessageID,
		IsRecalled:       false,
	}

	// 序列化 metadata
	if req.Metadata != nil {
		metadataBytes, err := json.Marshal(req.Metadata)
		if err != nil {
			return nil, fmt.Errorf("invalid metadata: %w", err)
		}
		message.Metadata = metadataBytes
	}

	// 7. 提前查询会话成员并检查查看状态（避免在事务内检查，提高准确性）
	var members []model.ConversationMember
	if err := s.db.Where("conversation_id = ? AND user_id != ?", conversationID, senderID).
		Find(&members).Error; err != nil {
		return nil, fmt.Errorf("failed to get conversation members: %w", err)
	}

	// 为每个成员记录是否正在查看会话（快照）
	memberViewingStatus := make(map[uuid.UUID]bool)
	for _, member := range members {
		isViewing := false
		if s.hubChecker != nil {
			isViewing = s.hubChecker.IsUserInConversation(member.UserID, conversationID)
		}
		memberViewingStatus[member.UserID] = isViewing
	}

	// 8. 使用事务保证数据一致性
	err = s.db.Transaction(func(tx *gorm.DB) error {
		// 8.1 保存消息
		if err := tx.Create(message).Error; err != nil {
			return fmt.Errorf("failed to save message: %w", err)
		}

		// 8.2 更新会话的最后消息
		now := time.Now()
		if err := tx.Model(&model.Conversation{}).Where("id = ?", conversationID).Updates(map[string]interface{}{
			"last_message_at": now,
			"last_message_id": message.ID,
			"updated_at":      now,
		}).Error; err != nil {
			return fmt.Errorf("failed to update conversation: %w", err)
		}

		// 8.3 更新未读计数并取消隐藏（基于之前的快照状态）
		for _, member := range members {
			isViewing := memberViewingStatus[member.UserID]

			// 构建更新字段
			updates := make(map[string]interface{})

			// 如果会话被隐藏，自动取消隐藏
			updates["is_hidden"] = false

			// 只有在用户不在该会话页面时才增加未读数
			if !isViewing {
				updates["unread_count"] = gorm.Expr("unread_count + ?", 1)
			}

			// 执行更新
			if err := tx.Model(&model.ConversationMember{}).
				Where("conversation_id = ? AND user_id = ?", conversationID, member.UserID).
				Updates(updates).Error; err != nil {
				return fmt.Errorf("failed to update conversation member: %w", err)
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	// 9. 生成消息预览文本
	var lastMessageText *string
	if message.MessageType == "text" && message.Content != nil {
		text := *message.Content
		runes := []rune(text)
		if len(runes) > 50 {
			text = string(runes[:50]) + "..."
		}
		lastMessageText = &text
	} else if message.MessageType == "image" {
		text := "[图片]"
		lastMessageText = &text
	} else if message.MessageType == "video" {
		text := "[视频]"
		lastMessageText = &text
	} else if message.MessageType == "emoji" {
		text := "[表情]"
		lastMessageText = &text
	}

	// 10. 将未读消息推送到 Redis（用于离线消息）并推送未读数量更新和会话更新
	// 使用之前查询的 members 和 memberViewingStatus（避免重新查询数据库）
	for _, member := range members {
		// 推送到离线消息队列，设置7天过期时间
		msgData, _ := json.Marshal(message)
		key := "offline_msg:" + member.UserID.String()
		pipe := s.rdb.Pipeline()
		pipe.RPush(ctx, key, msgData)
		pipe.Expire(ctx, key, 7*24*time.Hour) // 7天过期
		pipe.Exec(ctx)

		// 计算该成员的未读数（基于之前的快照状态）
		isViewing := memberViewingStatus[member.UserID]
		unreadCount := member.UnreadCount
		if !isViewing {
			unreadCount++ // 如果不在查看，未读数+1
		}

		// 推送会话更新（包含最新消息时间和未读数量）
		if s.convNotifier != nil && member.UserID != senderID {
			s.convNotifier.SendConversationUpdate(member.UserID, conversationID, &message.CreatedAt, lastMessageText, unreadCount)
		}

		// 实时推送未读数量更新（给在线用户）
		if s.unreadNotifier != nil {
			s.unreadNotifier.SendUnreadCountUpdate(member.UserID, conversationID, unreadCount)
		}

		// 注意：私信和群聊消息不创建通知
		// 通知功能保留用于系统通知等特殊场景
		// 用户可以通过会话列表的未读数量来了解新消息
	}

	return message, nil
}

// RecallMessage 撤回消息（2分钟内）
func (s *MessageService) RecallMessage(userID uuid.UUID, messageID uuid.UUID) error {
	var message model.Message
	if err := s.db.Where("id = ?", messageID).First(&message).Error; err != nil {
		return fmt.Errorf("message not found")
	}

	// 检查是否是发送者
	if message.SenderID != userID {
		return fmt.Errorf("you can only recall your own messages")
	}

	// 检查是否已撤回
	if message.IsRecalled {
		return fmt.Errorf("message already recalled")
	}

	// 检查是否超过2分钟（使用数据库原生计算，避免时区问题）
	var elapsedSeconds float64
	err := s.db.Raw(`
		SELECT EXTRACT(EPOCH FROM (NOW() - created_at))
		FROM messages
		WHERE id = ?
	`, messageID).Scan(&elapsedSeconds).Error

	if err != nil {
		return fmt.Errorf("failed to calculate elapsed time: %w", err)
	}

	if elapsedSeconds > 10 { // 2分钟 = 120秒
		return fmt.Errorf("can only recall messages within 2 minutes (elapsed: %.0f seconds)", elapsedSeconds)
	}

	// 撤回消息
	return s.db.Model(&message).Updates(map[string]interface{}{
		"is_recalled": true,
		"recalled_at": time.Now(),
	}).Error
}

// GetMessageByID 根据ID获取消息
func (s *MessageService) GetMessageByID(messageID uuid.UUID) (*model.Message, error) {
	var message model.Message
	if err := s.db.Where("id = ?", messageID).First(&message).Error; err != nil {
		return nil, fmt.Errorf("message not found")
	}
	return &message, nil
}

// SearchMessages 搜索消息
func (s *MessageService) SearchMessages(userID uuid.UUID, keyword string, conversationID *uuid.UUID, limit, offset int) ([]model.Message, error) {
	// 如果指定了 conversation_id，先检查用户是否是该会话成员
	if conversationID != nil {
		isMember, err := s.isConversationMember(*conversationID, userID)
		if err != nil {
			return nil, err
		}
		if !isMember {
			return nil, fmt.Errorf("you are not a member of this conversation")
		}
	}

	var messages []model.Message
	query := s.db.Table("messages").
		Select("DISTINCT messages.*").
		Joins("JOIN conversation_members ON messages.conversation_id = conversation_members.conversation_id").
		Where("conversation_members.user_id = ?", userID).
		Where("messages.content ILIKE ?", "%"+keyword+"%").
		Where("messages.is_recalled = ?", false)

	if conversationID != nil {
		query = query.Where("messages.conversation_id = ?", *conversationID)
	}

	if err := query.Order("messages.created_at DESC").Limit(limit).Offset(offset).Find(&messages).Error; err != nil {
		return nil, err
	}

	return messages, nil
}

// MarkAsRead 标记消息为已读（支持多设备，使用 MAX 逻辑确保幂等性）
func (s *MessageService) MarkAsRead(userID uuid.UUID, conversationID uuid.UUID, messageID uuid.UUID) error {
	// 先检查要标记的消息是否存在
	var targetMessage model.Message
	if err := s.db.Where("id = ?", messageID).First(&targetMessage).Error; err != nil {
		return fmt.Errorf("message not found: %w", err)
	}

	// 更新会话成员的已读状态（只有当新消息比当前 last_read 更新时才更新）
	// 使用原生 SQL 实现 MAX 逻辑，支持多设备并发标记
	result := s.db.Exec(`
		UPDATE conversation_members cm
		SET
			unread_count = 0,
			last_read_message_id = ?,
			last_read_at = NOW()
		WHERE cm.conversation_id = ?
		  AND cm.user_id = ?
		  AND (
		      cm.last_read_message_id IS NULL
		      OR NOT EXISTS (
		          SELECT 1 FROM messages m
		          WHERE m.id = cm.last_read_message_id
		            AND m.created_at > ?
		      )
		  )
	`, messageID, conversationID, userID, targetMessage.CreatedAt)

	if result.Error != nil {
		return result.Error
	}

	// 只有真正更新了记录，才推送未读数清零（避免旧消息标记触发推送）
	if result.RowsAffected > 0 && s.unreadNotifier != nil {
		s.unreadNotifier.SendUnreadCountUpdate(userID, conversationID, 0)
	}

	return nil
}

// getOrCreatePrivateConversation 获取或创建私聊会话（带分布式锁）
// 返回值: (conversationID, conversationJustCreated, error)
func (s *MessageService) getOrCreatePrivateConversation(user1ID, user2ID uuid.UUID) (uuid.UUID, bool, error) {
	ctx := context.Background()

	// 0. 检查是否是自己给自己发消息
	if user1ID == user2ID {
		return uuid.Nil, false, fmt.Errorf("cannot send message to yourself")
	}

	// 1. 先查询是否已存在
	var conversation model.Conversation
	err := s.db.Table("conversations c").
		Joins("INNER JOIN conversation_members m1 ON c.id = m1.conversation_id AND m1.user_id = ?", user1ID).
		Joins("INNER JOIN conversation_members m2 ON c.id = m2.conversation_id AND m2.user_id = ?", user2ID).
		Where("c.conversation_type = ?", "private").
		Where("(SELECT COUNT(*) FROM conversation_members WHERE conversation_id = c.id AND left_at IS NULL) = 2").
		First(&conversation).Error

	if err == nil {
		return conversation.ID, false, nil // 会话已存在，不是刚创建的
	}

	// 2. 使用 Redis 分布式锁（按用户ID排序生成锁key，确保顺序一致）
	smallerID, largerID := user1ID, user2ID
	if user1ID.String() > user2ID.String() {
		smallerID, largerID = user2ID, user1ID
	}
	lockKey := fmt.Sprintf("lock:create_conversation:%s:%s", smallerID, largerID)

	// 尝试获取锁（最多等待3秒）
	lockAcquired := false
	for i := 0; i < 30; i++ {
		ok, err := s.rdb.SetNX(ctx, lockKey, "1", 5*time.Second).Result()
		if err == nil && ok {
			lockAcquired = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !lockAcquired {
		return uuid.Nil, false, fmt.Errorf("failed to acquire lock for creating conversation")
	}

	defer s.rdb.Del(ctx, lockKey) // 释放锁

	// 3. 获得锁后，再次查询（可能已被其他请求创建）
	err = s.db.Table("conversations c").
		Joins("INNER JOIN conversation_members m1 ON c.id = m1.conversation_id AND m1.user_id = ?", user1ID).
		Joins("INNER JOIN conversation_members m2 ON c.id = m2.conversation_id AND m2.user_id = ?", user2ID).
		Where("c.conversation_type = ?", "private").
		Where("(SELECT COUNT(*) FROM conversation_members WHERE conversation_id = c.id AND left_at IS NULL) = 2").
		First(&conversation).Error

	if err == nil {
		return conversation.ID, false, nil // 会话已被其他请求创建
	}

	// 4. 确实不存在，创建新会话
	return s.createPrivateConversation(user1ID, user2ID)
}

// createPrivateConversation 创建私聊会话（带事务）
// 返回值: (conversationID, conversationJustCreated, error)
func (s *MessageService) createPrivateConversation(user1ID, user2ID uuid.UUID) (uuid.UUID, bool, error) {
	var conversationID uuid.UUID

	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 创建会话
		conversation := &model.Conversation{
			ConversationType: "private",
		}
		if err := tx.Create(conversation).Error; err != nil {
			return err
		}
		conversationID = conversation.ID

		// 添加两个成员
		for _, userID := range []uuid.UUID{user1ID, user2ID} {
			member := &model.ConversationMember{
				ConversationID: conversationID,
				UserID:         userID,
				Role:           "member",
			}
			if err := tx.Create(member).Error; err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return uuid.Nil, false, err
	}

	return conversationID, true, nil // 会话刚刚创建
}

// isConversationMember 检查用户是否是会话成员
func (s *MessageService) isConversationMember(conversationID, userID uuid.UUID) (bool, error) {
	var count int64
	err := s.db.Model(&model.ConversationMember{}).
		Where("conversation_id = ? AND user_id = ? AND left_at IS NULL", conversationID, userID).
		Count(&count).Error
	return count > 0, err
}

// isBlocked 检查是否被拉黑
func (s *MessageService) isBlocked(senderID, receiverID uuid.UUID) (bool, error) {
	var count int64
	err := s.db.Model(&model.UserRelationship{}).
		Where("user_id = ? AND target_user_id = ? AND relationship_type = ?", receiverID, senderID, "blocked").
		Count(&count).Error
	return count > 0, err
}

// getConversationMembers 获取会话成员列表
func (s *MessageService) getConversationMembers(conversationID uuid.UUID) ([]uuid.UUID, error) {
	var members []model.ConversationMember
	if err := s.db.Where("conversation_id = ? AND left_at IS NULL", conversationID).
		Find(&members).Error; err != nil {
		return nil, err
	}

	var userIDs []uuid.UUID
	for _, member := range members {
		userIDs = append(userIDs, member.UserID)
	}

	return userIDs, nil
}

// GetConversationMembers 导出方法供 handler 使用
func (s *MessageService) GetConversationMembers(conversationID uuid.UUID) ([]uuid.UUID, error) {
	return s.getConversationMembers(conversationID)
}

// CheckCanSend 检查用户是否可以发送消息（从消息历史判断）
func (s *MessageService) CheckCanSend(userID, conversationID uuid.UUID) bool {
	// 检查系统是否启用了首条消息限制功能
	if !s.sysSvc.IsFeatureEnabled("enable_first_message_limit") {
		return true
	}

	// 查询会话类型
	var conversation model.Conversation
	if err := s.db.Where("id = ?", conversationID).First(&conversation).Error; err != nil {
		return true // 查询失败，允许发送
	}

	// 群聊不受首条消息限制
	if conversation.ConversationType != "private" {
		return true
	}

	// 首条消息限制逻辑：
	// 1. 检查用户是否在这个会话中发过消息
	// 2. 如果发过，检查对方是否回复过
	// 3. 如果对方没回复过，不能继续发

	// 查询用户是否发过消息
	var userMessageCount int64
	s.db.Model(&model.Message{}).
		Where("conversation_id = ? AND sender_id = ?", conversationID, userID).
		Count(&userMessageCount)

	if userMessageCount == 0 {
		return true // 用户还没发过消息，可以发第一条
	}

	// 用户已经发过消息，检查对方是否回复过
	var otherUserMessageCount int64
	s.db.Model(&model.Message{}).
		Where("conversation_id = ? AND sender_id != ?", conversationID, userID).
		Count(&otherUserMessageCount)

	if otherUserMessageCount > 0 {
		return true // 对方回复过了，限制解除
	}

	// 对方还没回复，不能继续发
	return false
}

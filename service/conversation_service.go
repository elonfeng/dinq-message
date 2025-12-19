package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"dinq_message/model"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type ConversationService struct {
	db       *gorm.DB
	rdb      *redis.Client
	sysSvc   *SystemSettingsService
	agentURL string
}

func NewConversationService(db *gorm.DB) *ConversationService {
	agentURL := os.Getenv("AGENT_URL")
	if agentURL == "" {
		agentURL = "http://localhost:8082"
	}
	return &ConversationService{
		db:       db,
		rdb:      nil, // 可选，如果不需要在线状态功能可以为 nil
		sysSvc:   NewSystemSettingsService(db),
		agentURL: agentURL,
	}
}

func NewConversationServiceWithRedis(db *gorm.DB, rdb *redis.Client) *ConversationService {
	agentURL := os.Getenv("AGENT_URL")
	if agentURL == "" {
		agentURL = "http://localhost:8082"
	}
	return &ConversationService{
		db:       db,
		rdb:      rdb,
		sysSvc:   NewSystemSettingsService(db),
		agentURL: agentURL,
	}
}

// GetConversations 获取用户的所有会话列表(增强版)
func (s *ConversationService) GetConversations(userID uuid.UUID, limit, offset int, search string) ([]model.ConversationListItem, error) {
	// 1. 查询用户参与的会话ID列表(排除已隐藏的会话)
	type ConversationQuery struct {
		model.Conversation
		UnreadCount int `gorm:"column:unread_count"`
	}

	var (
		conversationQueries []ConversationQuery
		err                 error
	)

	search = strings.TrimSpace(search)
	if search == "" {
		if err = s.db.Table("conversations c").
			Select("c.*, cm.unread_count").
			Joins("INNER JOIN conversation_members cm ON c.id = cm.conversation_id AND cm.user_id = ?", userID).
			Where("cm.left_at IS NULL AND cm.is_hidden = ?", false).
			Order("c.last_message_at DESC NULLS LAST, c.created_at DESC").
			Limit(limit).
			Offset(offset).
			Find(&conversationQueries).Error; err != nil {
			return nil, fmt.Errorf("failed to query conversations: %w", err)
		}
	} else {
		var matchedIDs []uuid.UUID
		matchedIDs, err = s.filterConversationIDsBySearch(userID, search, limit, offset)
		if err != nil {
			return nil, err
		}
		if len(matchedIDs) == 0 {
			return []model.ConversationListItem{}, nil
		}

		if err = s.db.Table("conversations c").
			Select("c.*, cm.unread_count").
			Joins("INNER JOIN conversation_members cm ON c.id = cm.conversation_id AND cm.user_id = ?", userID).
			Where("cm.left_at IS NULL AND cm.is_hidden = ? AND c.id IN ?", false, matchedIDs).
			Order("c.last_message_at DESC NULLS LAST, c.created_at DESC").
			Find(&conversationQueries).Error; err != nil {
			return nil, fmt.Errorf("failed to query conversations: %w", err)
		}
	}

	if len(conversationQueries) == 0 {
		return []model.ConversationListItem{}, nil
	}

	// 2. 收集所有会话ID
	conversationIDs := make([]uuid.UUID, len(conversationQueries))
	conversationMap := make(map[uuid.UUID]*ConversationQuery)
	for i, convQuery := range conversationQueries {
		conversationIDs[i] = convQuery.ID
		convQueryCopy := convQuery
		conversationMap[convQuery.ID] = &convQueryCopy
	}

	// 3. 一次性查询所有会话的成员（解决 N+1 问题）
	var allMembers []model.ConversationMember
	err = s.db.Where("conversation_id IN ? AND left_at IS NULL", conversationIDs).
		Find(&allMembers).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query members: %w", err)
	}

	// 4. 按会话ID分组成员
	membersByConvID := make(map[uuid.UUID][]model.ConversationMember)
	for _, member := range allMembers {
		membersByConvID[member.ConversationID] = append(membersByConvID[member.ConversationID], member)
	}

	// 5. 收集所有有效的 last_message_id
	messageIDs := make([]uuid.UUID, 0)
	convIDToMessageID := make(map[uuid.UUID]uuid.UUID)
	for _, convID := range conversationIDs {
		convQuery := conversationMap[convID]
		if convQuery.LastMessageID != nil {
			messageIDs = append(messageIDs, *convQuery.LastMessageID)
			convIDToMessageID[convID] = *convQuery.LastMessageID
		}
	}

	// 6. 批量查询最新消息内容(直接用主键查询,高性能)
	lastMessageMap := s.getMessagesByIDs(messageIDs)

	// 7. 收集所有userID并批量查询用户信息
	userIDSet := make(map[string]bool)
	for _, members := range membersByConvID {
		for _, member := range members {
			userIDSet[member.UserID.String()] = true
		}
	}
	userIDs := make([]string, 0, len(userIDSet))
	for uid := range userIDSet {
		userIDs = append(userIDs, uid)
	}
	userDataMap := s.batchGetUserDataFromAgent(userIDs)

	// 8. 补充成员用户信息
	for convID, members := range membersByConvID {
		for i := range members {
			if userData, ok := userDataMap[members[i].UserID.String()]; ok {
				members[i].Name = &userData.Name
				members[i].AvatarURL = &userData.AvatarURL
				members[i].Username = &userData.Domain
				members[i].Position = &userData.Position
				members[i].Company = &userData.Company
			}
		}
		membersByConvID[convID] = members
	}

	// 9. 获取在线状态(仅私聊)
	ctx := context.Background()
	onlineStatusMap := make(map[uuid.UUID]map[string]bool)
	if s.rdb != nil && s.sysSvc.IsFeatureEnabled("enable_online_status") {
		for _, convID := range conversationIDs {
			convQuery := conversationMap[convID]
			if convQuery.ConversationType == "private" {
				members := membersByConvID[convID]
				onlineStatus := make(map[string]bool)
				for _, member := range members {
					if member.UserID != userID {
						key := "online:" + member.UserID.String()
						val, err := s.rdb.Get(ctx, key).Result()
						onlineStatus[member.UserID.String()] = (err == nil && val == "1")
					}
				}
				onlineStatusMap[convID] = onlineStatus
			}
		}
	}

	// 10. 组装结果
	conversations := make([]model.ConversationListItem, 0, len(conversationQueries))
	for _, convID := range conversationIDs {
		convQuery := conversationMap[convID]

		// 通过 convIDToMessageID 映射找到对应的消息内容
		var lastMsg *string
		if msgID, exists := convIDToMessageID[convID]; exists {
			lastMsg = lastMessageMap[msgID]
		}

		item := model.ConversationListItem{
			Conversation:    convQuery.Conversation,
			UnreadCount:     convQuery.UnreadCount,
			LastMessageTime: convQuery.LastMessageAt,
			LastMessageText: lastMsg,
			OnlineStatus:    onlineStatusMap[convID],
			Members:         membersByConvID[convID],
		}
		if item.Members == nil {
			item.Members = []model.ConversationMember{}
		}
		if item.OnlineStatus == nil {
			item.OnlineStatus = make(map[string]bool)
		}
		conversations = append(conversations, item)
	}

	return conversations, nil
}

func (s *ConversationService) filterConversationIDsBySearch(userID uuid.UUID, keyword string, limit, offset int) ([]uuid.UUID, error) {
	keywordLower := strings.ToLower(keyword)

	var conversations []model.Conversation
	err := s.db.Table("conversations").
		Select("DISTINCT conversations.*").
		Joins("INNER JOIN conversation_members cm1 ON conversations.id = cm1.conversation_id").
		Where("cm1.user_id = ? AND cm1.left_at IS NULL AND cm1.is_hidden = ? AND conversations.conversation_type = ?", userID, false, "private").
		Order("conversations.updated_at DESC, conversations.created_at DESC").
		Find(&conversations).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query conversations: %w", err)
	}

	if len(conversations) == 0 {
		return []uuid.UUID{}, nil
	}

	convIDs := make([]uuid.UUID, len(conversations))
	for i, conv := range conversations {
		convIDs[i] = conv.ID
	}

	var members []model.ConversationMember
	if err := s.db.Where("conversation_id IN ? AND user_id != ? AND left_at IS NULL", convIDs, userID).
		Find(&members).Error; err != nil {
		return nil, fmt.Errorf("failed to query members for search: %w", err)
	}

	conversationMembers := make(map[uuid.UUID][]uuid.UUID)
	userIDSet := make(map[string]struct{})
	for _, member := range members {
		conversationMembers[member.ConversationID] = append(conversationMembers[member.ConversationID], member.UserID)
		userIDSet[member.UserID.String()] = struct{}{}
	}

	userIDs := make([]string, 0, len(userIDSet))
	for uid := range userIDSet {
		userIDs = append(userIDs, uid)
	}
	userDataMap := s.batchGetUserDataFromAgent(userIDs)

	matched := make([]uuid.UUID, 0)
	for _, conv := range conversations {
		otherUsers := conversationMembers[conv.ID]
		for _, uid := range otherUsers {
			if userData, ok := userDataMap[uid.String()]; ok {
				nameLower := strings.ToLower(userData.Name)
				domainLower := strings.ToLower(userData.Domain)
				if strings.Contains(nameLower, keywordLower) || strings.Contains(domainLower, keywordLower) {
					matched = append(matched, conv.ID)
					break
				}
			}
		}
	}

	if offset >= len(matched) {
		return []uuid.UUID{}, nil
	}

	end := offset + limit
	if end > len(matched) {
		end = len(matched)
	}

	return matched[offset:end], nil
}

// GetConversationDetailWithMembers 获取单个会话并补齐成员信息（用于私聊接口）
func (s *ConversationService) GetConversationDetailWithMembers(conversationID, userID uuid.UUID) (*model.ConversationListItem, error) {
	type conversationQuery struct {
		model.Conversation
		UnreadCount int `gorm:"column:unread_count"`
	}

	var conv conversationQuery
	err := s.db.Table("conversations c").
		Select("c.*, cm.unread_count").
		Joins("INNER JOIN conversation_members cm ON c.id = cm.conversation_id AND cm.user_id = ?", userID).
		Where("c.id = ? AND cm.left_at IS NULL", conversationID).
		First(&conv).Error
	if err != nil {
		return nil, fmt.Errorf("failed to load conversation: %w", err)
	}

	// 查询成员
	var members []model.ConversationMember
	if err := s.db.Where("conversation_id = ? AND left_at IS NULL", conversationID).
		Find(&members).Error; err != nil {
		return nil, fmt.Errorf("failed to load members: %w", err)
	}

	// 补充用户信息
	userIDs := make([]string, 0, len(members))
	for _, m := range members {
		userIDs = append(userIDs, m.UserID.String())
	}
	userDataMap := s.batchGetUserDataFromAgent(userIDs)
	for i := range members {
		if userData, ok := userDataMap[members[i].UserID.String()]; ok {
			members[i].Name = &userData.Name
			members[i].AvatarURL = &userData.AvatarURL
			members[i].Username = &userData.Domain
			members[i].Position = &userData.Position
			members[i].Company = &userData.Company
		}
	}

	// 获取最新消息文本
	var lastMsgText *string
	if conv.LastMessageID != nil {
		lastMap := s.getMessagesByIDs([]uuid.UUID{*conv.LastMessageID})
		lastMsgText = lastMap[*conv.LastMessageID]
	}

	// 在线状态（仅私聊）
	onlineStatus := make(map[string]bool)
	if s.rdb != nil && conv.ConversationType == "private" && s.sysSvc.IsFeatureEnabled("enable_online_status") {
		ctx := context.Background()
		for _, member := range members {
			if member.UserID == userID {
				continue
			}
			key := "online:" + member.UserID.String()
			val, err := s.rdb.Get(ctx, key).Result()
			onlineStatus[member.UserID.String()] = (err == nil && val == "1")
		}
	}

	return &model.ConversationListItem{
		Conversation:    conv.Conversation,
		UnreadCount:     conv.UnreadCount,
		LastMessageTime: conv.LastMessageAt,
		LastMessageText: lastMsgText,
		Members:         members,
		OnlineStatus:    onlineStatus,
	}, nil
}

// getMessagesByIDs 批量根据消息ID查询消息内容(用主键查询,高性能)
func (s *ConversationService) getMessagesByIDs(messageIDs []uuid.UUID) map[uuid.UUID]*string {
	result := make(map[uuid.UUID]*string)

	if len(messageIDs) == 0 {
		return result
	}

	type MessagePreview struct {
		ID          uuid.UUID
		Content     *string
		MessageType string
	}

	var messages []MessagePreview
	// 直接用主键IN查询,利用主键索引,性能最优
	err := s.db.Table("messages").
		Select("id, content, message_type").
		Where("id IN ? AND is_recalled = ?", messageIDs, false).
		Find(&messages).Error

	if err != nil {
		return result
	}

	for _, msg := range messages {
		var text string
		if msg.MessageType == "text" && msg.Content != nil {
			text = *msg.Content
			// 限制预览长度
			if len(text) > 50 {
				text = text[:50] + "..."
			}
		} else if msg.MessageType == "image" {
			text = "[图片]"
		} else if msg.MessageType == "video" {
			text = "[视频]"
		} else if msg.MessageType == "emoji" {
			text = "[表情]"
		}
		result[msg.ID] = &text
	}

	return result
}

// GetMessages 获取会话的消息历史（包含 can_send 状态和在线状态）
func (s *ConversationService) GetMessages(userID, conversationID uuid.UUID, limit, offset int) (map[string]interface{}, error) {
	// 检查用户是否是会话成员
	isMember, err := s.isConversationMember(conversationID, userID)
	if err != nil || !isMember {
		return nil, fmt.Errorf("user is not a member of this conversation")
	}

	var messages []model.Message
	err = s.db.Where("conversation_id = ?", conversationID).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&messages).Error

	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}

	// 反转顺序，使最新消息在最后
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	// 计算是否可以发送消息
	canSend := s.checkCanSendFromMessages(userID, messages)

	// 获取会话成员的在线状态（仅私聊）
	onlineStatus := s.getOnlineStatusForConversation(userID, conversationID)

	return map[string]interface{}{
		"messages":      messages,
		"can_send":      canSend,
		"online_status": onlineStatus,
	}, nil
}

// checkCanSendFromMessages 从消息列表判断用户是否可以发送消息
func (s *ConversationService) checkCanSendFromMessages(userID uuid.UUID, messages []model.Message) bool {
	// 如果系统未启用首条消息限制，直接返回 true
	if !s.sysSvc.IsFeatureEnabled("enable_first_message_limit") {
		return true
	}

	myMessageCount := 0
	othersMessageCount := 0

	for _, msg := range messages {
		if msg.SenderID == userID {
			myMessageCount++
		} else {
			othersMessageCount++
			break // 只要有一条对方的消息就够了
		}
	}

	// 还没发过消息，或者对方已回复
	return myMessageCount == 0 || othersMessageCount > 0
}

// CreateGroupConversation 创建群聊
func (s *ConversationService) CreateGroupConversation(creatorID uuid.UUID, groupName string, memberIDs []uuid.UUID) (*model.Conversation, error) {
	var conversation *model.Conversation

	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 创建会话
		conversation = &model.Conversation{
			ConversationType: "group",
			GroupName:        &groupName,
		}
		if err := tx.Create(conversation).Error; err != nil {
			return fmt.Errorf("failed to create conversation: %w", err)
		}

		// 添加创建者为 owner
		owner := &model.ConversationMember{
			ConversationID: conversation.ID,
			UserID:         creatorID,
			Role:           "owner",
		}
		if err := tx.Create(owner).Error; err != nil {
			return err
		}

		// 添加其他成员
		for _, memberID := range memberIDs {
			if memberID == creatorID {
				continue // 跳过创建者
			}

			member := &model.ConversationMember{
				ConversationID: conversation.ID,
				UserID:         memberID,
				Role:           "member",
			}
			if err := tx.Create(member).Error; err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return conversation, nil
}

// isConversationMember 检查用户是否是会话成员
func (s *ConversationService) isConversationMember(conversationID, userID uuid.UUID) (bool, error) {
	var count int64
	err := s.db.Model(&model.ConversationMember{}).
		Where("conversation_id = ? AND user_id = ? AND left_at IS NULL", conversationID, userID).
		Count(&count).Error
	return count > 0, err
}

// getConversationMembers 获取会话成员
func (s *ConversationService) getConversationMembers(conversationID uuid.UUID) ([]model.ConversationMember, error) {
	var members []model.ConversationMember
	err := s.db.Where("conversation_id = ? AND left_at IS NULL", conversationID).
		Find(&members).Error
	if err != nil {
		return nil, err
	}
	return members, nil
}

// getOnlineStatusForConversation 获取会话成员的在线状态（仅私聊）
func (s *ConversationService) getOnlineStatusForConversation(currentUserID, conversationID uuid.UUID) map[string]bool {
	onlineStatus := make(map[string]bool)

	// 如果没有 Redis 或未启用在线状态功能，返回空 map
	if s.rdb == nil || !s.sysSvc.IsFeatureEnabled("enable_online_status") {
		return onlineStatus
	}

	// 检查会话类型
	var conversation model.Conversation
	if err := s.db.Where("id = ?", conversationID).First(&conversation).Error; err != nil {
		return onlineStatus
	}

	// 只有私聊才返回在线状态，群聊返回空 map
	if conversation.ConversationType != "private" {
		return onlineStatus
	}

	// 获取会话成员
	members, err := s.getConversationMembers(conversationID)
	if err != nil {
		return onlineStatus
	}

	// 只查询对方的在线状态（私聊只有2个成员）
	ctx := context.Background()
	for _, member := range members {
		// 跳过自己，只查对方
		if member.UserID == currentUserID {
			continue
		}

		key := "online:" + member.UserID.String()
		val, err := s.rdb.Get(ctx, key).Result()
		onlineStatus[member.UserID.String()] = (err == nil && val == "1")
	}

	return onlineStatus
}

// AddMembersToGroup 添加群聊成员
func (s *ConversationService) AddMembersToGroup(userID, conversationID uuid.UUID, memberIDs []uuid.UUID) error {
	// 检查是否是群聊
	var conversation model.Conversation
	if err := s.db.Where("id = ?", conversationID).First(&conversation).Error; err != nil {
		return fmt.Errorf("conversation not found")
	}
	if conversation.ConversationType != "group" {
		return fmt.Errorf("can only add members to group conversations")
	}

	// 检查操作者权限（必须是 owner 或 admin）
	var member model.ConversationMember
	err := s.db.Where("conversation_id = ? AND user_id = ? AND left_at IS NULL", conversationID, userID).
		First(&member).Error
	if err != nil {
		return fmt.Errorf("you are not a member of this conversation")
	}
	if member.Role != "owner" && member.Role != "admin" {
		return fmt.Errorf("only owner or admin can add members")
	}

	// 添加成员
	now := time.Now()
	for _, memberID := range memberIDs {
		// 检查是否已经是成员
		var count int64
		s.db.Model(&model.ConversationMember{}).
			Where("conversation_id = ? AND user_id = ? AND left_at IS NULL", conversationID, memberID).
			Count(&count)

		if count > 0 {
			continue // 已经是成员，跳过
		}

		// 添加成员
		newMember := &model.ConversationMember{
			ConversationID: conversationID,
			UserID:         memberID,
			Role:           "member",
			JoinedAt:       now,
		}
		if err := s.db.Create(newMember).Error; err != nil {
			return fmt.Errorf("failed to add member: %w", err)
		}
	}

	return nil
}

// RemoveMemberFromGroup 移除群聊成员
func (s *ConversationService) RemoveMemberFromGroup(userID, conversationID, targetUserID uuid.UUID) error {
	// 检查是否是群聊
	var conversation model.Conversation
	if err := s.db.Where("id = ?", conversationID).First(&conversation).Error; err != nil {
		return fmt.Errorf("conversation not found")
	}
	if conversation.ConversationType != "group" {
		return fmt.Errorf("can only remove members from group conversations")
	}

	// 检查操作者权限
	var operatorMember model.ConversationMember
	err := s.db.Where("conversation_id = ? AND user_id = ? AND left_at IS NULL", conversationID, userID).
		First(&operatorMember).Error
	if err != nil {
		return fmt.Errorf("you are not a member of this conversation")
	}
	if operatorMember.Role != "owner" && operatorMember.Role != "admin" {
		return fmt.Errorf("only owner or admin can remove members")
	}

	// 不能移除 owner
	var targetMember model.ConversationMember
	err = s.db.Where("conversation_id = ? AND user_id = ? AND left_at IS NULL", conversationID, targetUserID).
		First(&targetMember).Error
	if err != nil {
		return fmt.Errorf("target user is not a member")
	}
	if targetMember.Role == "owner" {
		return fmt.Errorf("cannot remove owner")
	}

	// 移除成员（标记为已离开）
	now := time.Now()
	return s.db.Model(&targetMember).Update("left_at", now).Error
}

// LeaveGroup 离开群聊
func (s *ConversationService) LeaveGroup(userID, conversationID uuid.UUID) error {
	// 检查是否是群聊
	var conversation model.Conversation
	if err := s.db.Where("id = ?", conversationID).First(&conversation).Error; err != nil {
		return fmt.Errorf("conversation not found")
	}
	if conversation.ConversationType != "group" {
		return fmt.Errorf("can only leave group conversations")
	}

	// 检查是否是 owner
	var member model.ConversationMember
	err := s.db.Where("conversation_id = ? AND user_id = ? AND left_at IS NULL", conversationID, userID).
		First(&member).Error
	if err != nil {
		return fmt.Errorf("you are not a member of this conversation")
	}
	if member.Role == "owner" {
		return fmt.Errorf("owner cannot leave group, please transfer ownership first")
	}

	// 标记为已离开
	now := time.Now()
	return s.db.Model(&member).Update("left_at", now).Error
}

// UpdateMemberRole 更新成员角色
func (s *ConversationService) UpdateMemberRole(userID, conversationID, targetUserID uuid.UUID, newRole string) error {
	// 检查角色有效性
	if newRole != "owner" && newRole != "admin" && newRole != "member" {
		return fmt.Errorf("invalid role")
	}

	// 检查操作者必须是 owner
	var operatorMember model.ConversationMember
	err := s.db.Where("conversation_id = ? AND user_id = ? AND left_at IS NULL", conversationID, userID).
		First(&operatorMember).Error
	if err != nil {
		return fmt.Errorf("you are not a member of this conversation")
	}
	if operatorMember.Role != "owner" {
		return fmt.Errorf("only owner can change roles")
	}

	// 更新角色
	result := s.db.Model(&model.ConversationMember{}).
		Where("conversation_id = ? AND user_id = ? AND left_at IS NULL", conversationID, targetUserID).
		Update("role", newRole)

	if result.Error != nil {
		return fmt.Errorf("failed to update role: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("member not found")
	}

	return nil
}

// HideConversation 隐藏会话(软删除,收到新消息时自动恢复)
func (s *ConversationService) HideConversation(userID, conversationID uuid.UUID) error {
	result := s.db.Model(&model.ConversationMember{}).
		Where("conversation_id = ? AND user_id = ? AND left_at IS NULL", conversationID, userID).
		Update("is_hidden", true)

	if result.Error != nil {
		return fmt.Errorf("failed to hide conversation: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("conversation not found or already left")
	}

	return nil
}

// UnhideConversation 恢复会话显示(收到新消息时自动调用)
func (s *ConversationService) UnhideConversation(userID, conversationID uuid.UUID) error {
	result := s.db.Model(&model.ConversationMember{}).
		Where("conversation_id = ? AND user_id = ? AND left_at IS NULL", conversationID, userID).
		Update("is_hidden", false)

	if result.Error != nil {
		return fmt.Errorf("failed to unhide conversation: %w", result.Error)
	}

	return nil
}

// UserDataInfo 用户数据信息
type UserDataInfo struct {
	Name      string
	AvatarURL string
	Domain    string // 用户域名（原 username）
	Position  string
	Company   string
}

// batchGetUserDataFromAgent 从agent批量获取用户数据
func (s *ConversationService) batchGetUserDataFromAgent(userIDs []string) map[string]UserDataInfo {
	result := make(map[string]UserDataInfo)

	if len(userIDs) == 0 {
		return result
	}

	// 构造请求
	reqBody, err := json.Marshal(map[string]interface{}{
		"user_ids": userIDs,
	})
	if err != nil {
		return result
	}

	// 调用agent接口
	resp, err := http.Post(s.agentURL+"/api/v1/user-data/batch", "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return result
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return result
	}

	// 解析响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return result
	}

	var apiResp struct {
		Code int `json:"code"`
		Data map[string]struct {
			Name      string `json:"name"`
			AvatarURL string `json:"avatar_url"`
			Domain    string `json:"domain"`
			Position  string `json:"position"`
			Company   string `json:"company"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &apiResp); err != nil {
		return result
	}

	if apiResp.Code == 0 && apiResp.Data != nil {
		for uid, data := range apiResp.Data {
			result[uid] = UserDataInfo{
				Name:      data.Name,
				AvatarURL: data.AvatarURL,
				Domain:    data.Domain,
				Position:  data.Position,
				Company:   data.Company,
			}
		}
	}

	return result
}

// SearchConversations 搜索会话（根据对方用户名模糊匹配）
func (s *ConversationService) SearchConversations(userID uuid.UUID, keyword string, limit, offset int) ([]map[string]interface{}, error) {
	fmt.Printf("[SearchConversations] Start: userID=%s, keyword=%s\n", userID, keyword)

	// 1. 获取用户参与的所有私聊会话
	var conversations []model.Conversation
	err := s.db.Table("conversations").
		Select("DISTINCT conversations.*").
		Joins("INNER JOIN conversation_members cm1 ON conversations.id = cm1.conversation_id").
		Where("cm1.user_id = ? AND cm1.left_at IS NULL AND conversations.conversation_type = ?", userID, "private").
		Order("conversations.updated_at DESC").
		Find(&conversations).Error

	if err != nil {
		return nil, err
	}

	fmt.Printf("[SearchConversations] Found %d conversations\n", len(conversations))

	// 2. 获取所有对方用户的 ID
	var otherUserIDs []string
	conversationUserMap := make(map[string]uuid.UUID) // map[otherUserID]conversationID

	for _, conv := range conversations {
		// 查询对方用户 ID
		var members []model.ConversationMember
		s.db.Where("conversation_id = ? AND user_id != ? AND left_at IS NULL", conv.ID, userID).
			Find(&members)

		for _, member := range members {
			otherUserIDs = append(otherUserIDs, member.UserID.String())
			conversationUserMap[member.UserID.String()] = conv.ID
			fmt.Printf("[SearchConversations] Found other user: %s in conversation %s\n", member.UserID, conv.ID)
		}
	}

	if len(otherUserIDs) == 0 {
		return []map[string]interface{}{}, nil
	}

	// 3. 从 Agent 获取所有对方用户的数据（保持与会话列表一致）
	userDataMap := s.batchGetUserDataFromAgent(otherUserIDs)
	fmt.Printf("[SearchConversations] Got %d users from agent, keyword=%s\n", len(userDataMap), keyword)

	// 4. 在内存中过滤匹配关键词的用户（不区分大小写）
	keywordLower := strings.ToLower(keyword)
	matchedUserIDs := make([]string, 0)
	for uid, userData := range userDataMap {
		fmt.Printf("[SearchConversations] Checking user %s: name=%s\n", uid, userData.Name)
		if strings.Contains(strings.ToLower(userData.Name), keywordLower) {
			fmt.Printf("[SearchConversations] MATCHED: user %s\n", uid)
			matchedUserIDs = append(matchedUserIDs, uid)
		}
	}

	fmt.Printf("[SearchConversations] Found %d matched users\n", len(matchedUserIDs))

	if len(matchedUserIDs) == 0 {
		return []map[string]interface{}{}, nil
	}

	// 5. 构造返回结果
	var results []map[string]interface{}
	for _, uid := range matchedUserIDs {
		userData := userDataMap[uid]
		convID := conversationUserMap[uid]

		// 查询会话详情
		var conv model.Conversation
		if err := s.db.Where("id = ?", convID).First(&conv).Error; err != nil {
			continue
		}

		// 查询最新消息
		var lastMessage model.Message
		s.db.Where("conversation_id = ?", convID).
			Order("created_at DESC").
			First(&lastMessage)

		// 查询未读数量
		var member model.ConversationMember
		s.db.Where("conversation_id = ? AND user_id = ?", convID, userID).
			First(&member)

		result := map[string]interface{}{
			"conversation_id":   convID,
			"conversation_type": conv.ConversationType,
			"other_user": map[string]interface{}{
				"user_id":    uid,
				"name":       userData.Name,
				"avatar_url": userData.AvatarURL,
				"position":   userData.Position,
				"company":    userData.Company,
			},
			"last_message_time": conv.UpdatedAt,
			"unread_count":      member.UnreadCount,
			"updated_at":        conv.UpdatedAt,
		}

		if lastMessage.ID != uuid.Nil {
			result["last_message"] = map[string]interface{}{
				"content":      lastMessage.Content,
				"message_type": lastMessage.MessageType,
				"sender_id":    lastMessage.SenderID,
			}
		}

		results = append(results, result)
	}

	// 5. 应用分页
	if offset >= len(results) {
		return []map[string]interface{}{}, nil
	}
	end := offset + limit
	if end > len(results) {
		end = len(results)
	}

	return results[offset:end], nil
}

// CreateOrGetPrivateConversation 创建或获取私聊会话（HTTP 接口专用）
// 返回值: (conversation, isNewlyCreated, error)
func (s *ConversationService) CreateOrGetPrivateConversation(user1ID, user2ID uuid.UUID) (*model.Conversation, bool, error) {
	ctx := context.Background()

	// 0. 检查是否是自己给自己发消息
	if user1ID == user2ID {
		return nil, false, fmt.Errorf("cannot create conversation with yourself")
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
		return &conversation, false, nil // 会话已存在，不是新创建的
	}

	// 2. 如果没有 Redis，直接创建
	if s.rdb == nil {
		return s.createPrivateConversationWithoutLock(user1ID, user2ID)
	}

	// 3. 使用 Redis 分布式锁（按用户ID排序生成锁key，确保顺序一致）
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
		return nil, false, fmt.Errorf("failed to acquire lock for creating conversation")
	}

	defer s.rdb.Del(ctx, lockKey) // 释放锁

	// 4. 获得锁后，再次查询（可能已被其他请求创建）
	err = s.db.Table("conversations c").
		Joins("INNER JOIN conversation_members m1 ON c.id = m1.conversation_id AND m1.user_id = ?", user1ID).
		Joins("INNER JOIN conversation_members m2 ON c.id = m2.conversation_id AND m2.user_id = ?", user2ID).
		Where("c.conversation_type = ?", "private").
		Where("(SELECT COUNT(*) FROM conversation_members WHERE conversation_id = c.id AND left_at IS NULL) = 2").
		First(&conversation).Error

	if err == nil {
		return &conversation, false, nil // 会话已被其他请求创建
	}

	// 5. 确实不存在，创建新会话
	return s.createPrivateConversationWithoutLock(user1ID, user2ID)
}

// createPrivateConversationWithoutLock 创建私聊会话（带事务，不含锁）
func (s *ConversationService) createPrivateConversationWithoutLock(user1ID, user2ID uuid.UUID) (*model.Conversation, bool, error) {
	var conversation *model.Conversation

	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 创建会话
		conversation = &model.Conversation{
			ConversationType: "private",
		}
		if err := tx.Create(conversation).Error; err != nil {
			return err
		}

		// 添加两个成员
		for _, userID := range []uuid.UUID{user1ID, user2ID} {
			member := &model.ConversationMember{
				ConversationID: conversation.ID,
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
		return nil, false, fmt.Errorf("failed to create conversation: %w", err)
	}

	return conversation, true, nil
}

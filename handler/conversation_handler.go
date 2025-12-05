package handler

import (
	"strconv"

	"dinq_message/middleware"
	"dinq_message/service"
	"dinq_message/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type ConversationHandler struct {
	convSvc *service.ConversationService
}

func NewConversationHandler(convSvc *service.ConversationService) *ConversationHandler {
	return &ConversationHandler{convSvc: convSvc}
}

// GetConversations 获取会话列表
func (h *ConversationHandler) GetConversations(c *gin.Context) {
	userID, exists := middleware.GetUserID(c)
	if !exists {
		utils.Unauthorized(c, "unauthorized")
		return
	}

	// 分页参数
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	search := c.Query("search")

	conversations, err := h.convSvc.GetConversations(userID, limit, offset, search)
	if err != nil {
		utils.InternalServerError(c, err.Error())
		return
	}

	utils.SuccessResponse(c, gin.H{"conversations": conversations})
}

// GetMessages 获取消息历史
func (h *ConversationHandler) GetMessages(c *gin.Context) {
	userID, exists := middleware.GetUserID(c)
	if !exists {
		utils.Unauthorized(c, "unauthorized")
		return
	}

	conversationID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.BadRequest(c, "invalid conversation id")
		return
	}

	// 分页参数
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

	result, err := h.convSvc.GetMessages(userID, conversationID, limit, offset)
	if err != nil {
		utils.Forbidden(c, err.Error())
		return
	}

	// result 已经包含 messages、can_send 和 online_status
	utils.SuccessResponse(c, result)
}

// CreateGroup 创建群聊
func (h *ConversationHandler) CreateGroup(c *gin.Context) {
	userID, exists := middleware.GetUserID(c)
	if !exists {
		utils.Unauthorized(c, "unauthorized")
		return
	}

	var req struct {
		GroupName string      `json:"group_name" binding:"required"`
		MemberIDs []uuid.UUID `json:"member_ids" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}

	conversation, err := h.convSvc.CreateGroupConversation(userID, req.GroupName, req.MemberIDs)
	if err != nil {
		utils.InternalServerError(c, err.Error())
		return
	}

	utils.SuccessResponse(c, conversation)
}

// AddMembers 添加群聊成员
func (h *ConversationHandler) AddMembers(c *gin.Context) {
	userID, exists := middleware.GetUserID(c)
	if !exists {
		utils.Unauthorized(c, "unauthorized")
		return
	}

	conversationID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.BadRequest(c, "invalid conversation id")
		return
	}

	var req struct {
		MemberIDs []uuid.UUID `json:"member_ids" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}

	if err := h.convSvc.AddMembersToGroup(userID, conversationID, req.MemberIDs); err != nil {
		utils.Forbidden(c, err.Error())
		return
	}

	utils.SuccessWithMessage(c, "members added successfully", nil)
}

// RemoveMember 移除群聊成员
func (h *ConversationHandler) RemoveMember(c *gin.Context) {
	userID, exists := middleware.GetUserID(c)
	if !exists {
		utils.Unauthorized(c, "unauthorized")
		return
	}

	conversationID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.BadRequest(c, "invalid conversation id")
		return
	}

	var req struct {
		UserID uuid.UUID `json:"user_id" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}

	if err := h.convSvc.RemoveMemberFromGroup(userID, conversationID, req.UserID); err != nil {
		utils.Forbidden(c, err.Error())
		return
	}

	utils.SuccessWithMessage(c, "member removed successfully", nil)
}

// LeaveGroup 离开群聊
func (h *ConversationHandler) LeaveGroup(c *gin.Context) {
	userID, exists := middleware.GetUserID(c)
	if !exists {
		utils.Unauthorized(c, "unauthorized")
		return
	}

	conversationID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.BadRequest(c, "invalid conversation id")
		return
	}

	if err := h.convSvc.LeaveGroup(userID, conversationID); err != nil {
		utils.Forbidden(c, err.Error())
		return
	}

	utils.SuccessWithMessage(c, "left group successfully", nil)
}

// UpdateMemberRole 更新成员角色
func (h *ConversationHandler) UpdateMemberRole(c *gin.Context) {
	userID, exists := middleware.GetUserID(c)
	if !exists {
		utils.Unauthorized(c, "unauthorized")
		return
	}

	conversationID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.BadRequest(c, "invalid conversation id")
		return
	}

	targetUserID, err := uuid.Parse(c.Param("user_id"))
	if err != nil {
		utils.BadRequest(c, "invalid user id")
		return
	}

	var req struct {
		Role string `json:"role" binding:"required,oneof=owner admin member"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}

	if err := h.convSvc.UpdateMemberRole(userID, conversationID, targetUserID, req.Role); err != nil {
		utils.Forbidden(c, err.Error())
		return
	}

	utils.SuccessWithMessage(c, "role updated successfully", nil)
}

// HideConversation 隐藏会话(软删除)
func (h *ConversationHandler) HideConversation(c *gin.Context) {
	userID, exists := middleware.GetUserID(c)
	if !exists {
		utils.Unauthorized(c, "unauthorized")
		return
	}

	conversationID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.BadRequest(c, "invalid conversation id")
		return
	}

	if err := h.convSvc.HideConversation(userID, conversationID); err != nil {
		utils.Forbidden(c, err.Error())
		return
	}

	utils.SuccessWithMessage(c, "conversation hidden successfully", nil)
}

// SearchConversations 搜索会话
func (h *ConversationHandler) SearchConversations(c *gin.Context) {
	userID, exists := middleware.GetUserID(c)
	if !exists {
		utils.Unauthorized(c, "unauthorized")
		return
	}

	// 获取搜索关键词
	keyword := c.Query("q")
	if keyword == "" {
		keyword = c.Query("keyword") // 兼容
	}

	if keyword == "" {
		utils.BadRequest(c, "q or keyword is required")
		return
	}

	// 分页参数
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

	results, err := h.convSvc.SearchConversations(userID, keyword, limit, offset)
	if err != nil {
		utils.InternalServerError(c, err.Error())
		return
	}

	utils.SuccessResponse(c, gin.H{
		"conversations": results,
		"total":         len(results),
	})
}

// CreatePrivateConversation 创建或获取私聊会话
func (h *ConversationHandler) CreatePrivateConversation(c *gin.Context) {
	userID, exists := middleware.GetUserID(c)
	if !exists {
		utils.Unauthorized(c, "unauthorized")
		return
	}

	var req struct {
		ReceiverID uuid.UUID `json:"receiver_id" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}

	conversation, isNewlyCreated, err := h.convSvc.CreateOrGetPrivateConversation(userID, req.ReceiverID)
	if err != nil {
		utils.BadRequest(c, err.Error())
		return
	}

	detail, err := h.convSvc.GetConversationDetailWithMembers(conversation.ID, userID)
	if err != nil {
		utils.InternalServerError(c, err.Error())
		return
	}

	utils.SuccessResponse(c, gin.H{
		"conversation":     detail,
		"is_newly_created": isNewlyCreated,
	})
}

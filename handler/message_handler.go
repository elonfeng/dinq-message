package handler

import (
	"encoding/json"
	"strconv"
	"strings"

	"dinq_message/service"
	"dinq_message/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type MessageHandler struct {
	msgSvc *service.MessageService
	hub    *Hub
}

func NewMessageHandler(msgSvc *service.MessageService, hub *Hub) *MessageHandler {
	return &MessageHandler{
		msgSvc: msgSvc,
		hub:    hub,
	}
}

// RecallMessage 撤回消息
func (h *MessageHandler) RecallMessage(c *gin.Context) {
	// 从URL参数获取消息ID
	msgIDStr := c.Param("id")
	msgID, err := uuid.Parse(msgIDStr)
	if err != nil {
		utils.BadRequest(c, "Invalid message ID")
		return
	}

	// 从上下文获取用户ID（由认证中间件设置）
	userID, exists := c.Get("user_id")
	if !exists {
		utils.Unauthorized(c, "Unauthorized")
		return
	}

	// 先查询消息获取conversation_id（用于广播）
	message, err := h.msgSvc.GetMessageByID(msgID)
	if err != nil {
		utils.NotFound(c, "Message not found")
		return
	}

	// 撤回消息
	if err := h.msgSvc.RecallMessage(userID.(uuid.UUID), msgID); err != nil {
		// 根据错误类型返回不同的状态码
		errMsg := err.Error()
		if errMsg == "message not found" {
			utils.NotFound(c, errMsg)
		} else if errMsg == "you can only recall your own messages" {
			utils.Forbidden(c, errMsg)
		} else if strings.Contains(errMsg, "can only recall messages within 2 minutes") {
			utils.BadRequest(c, errMsg)
		} else if errMsg == "message already recalled" {
			utils.BadRequest(c, errMsg)
		} else {
			utils.InternalServerError(c, errMsg)
		}
		return
	}

	// 广播撤回通知给会话中的所有在线成员
	response := map[string]interface{}{
		"type": "recalled",
		"data": map[string]interface{}{
			"message_id": msgID,
		},
	}
	responseData, _ := json.Marshal(response)

	members, err := h.msgSvc.GetConversationMembers(message.ConversationID)
	if err == nil {
		for _, memberID := range members {
			h.hub.BroadcastToUser(memberID, responseData)
		}
	}

	utils.SuccessWithMessage(c, "Message recalled successfully", nil)
}

// SearchMessages 搜索消息
func (h *MessageHandler) SearchMessages(c *gin.Context) {
	// 从上下文获取用户ID
	userID, exists := c.Get("user_id")
	if !exists {
		utils.Unauthorized(c, "Unauthorized")
		return
	}

	// 获取搜索参数
	keyword := c.Query("q")
	if keyword == "" {
		keyword = c.Query("keyword") // 兼容 keyword 参数
	}
	conversationIDStr := c.Query("conversation_id")

	if keyword == "" {
		utils.BadRequest(c, "q or keyword is required")
		return
	}

	var conversationID *uuid.UUID
	if conversationIDStr != "" {
		id, err := uuid.Parse(conversationIDStr)
		if err != nil {
			utils.BadRequest(c, "Invalid conversation_id")
			return
		}
		conversationID = &id
	}

	// 获取分页参数
	limit := 50 // 默认50条
	offset := 0
	if limitStr := c.Query("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}
	if offsetStr := c.Query("offset"); offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	// 调用 service 层搜索消息
	messages, err := h.msgSvc.SearchMessages(userID.(uuid.UUID), keyword, conversationID, limit, offset)
	if err != nil {
		// 根据错误类型返回不同的状态码
		errMsg := err.Error()
		if errMsg == "you are not a member of this conversation" {
			utils.Forbidden(c, errMsg)
		} else {
			utils.InternalServerError(c, errMsg)
		}
		return
	}

	utils.SuccessResponse(c, gin.H{
		"messages": messages,
	})
}

package handler

import (
	"strconv"

	"dinq_message/middleware"
	"dinq_message/service"
	"dinq_message/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type NotificationHandler struct {
	notifSvc *service.NotificationService
}

func NewNotificationHandler(notifSvc *service.NotificationService) *NotificationHandler {
	return &NotificationHandler{notifSvc: notifSvc}
}

// GetNotifications 获取通知列表
func (h *NotificationHandler) GetNotifications(c *gin.Context) {
	userID, exists := middleware.GetUserID(c)
	if !exists {
		utils.Unauthorized(c, "unauthorized")
		return
	}

	// 分页参数
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	unreadOnly := c.DefaultQuery("unread_only", "false") == "true"

	notifications, err := h.notifSvc.GetNotifications(userID, limit, offset, unreadOnly)
	if err != nil {
		utils.InternalServerError(c, err.Error())
		return
	}

	// 获取通知摘要(未读数量+最新通知时间)
	summary, _ := h.notifSvc.GetNotificationSummary(userID)

	utils.SuccessResponse(c, gin.H{
		"notifications":     notifications,
		"unread_count":      summary["unread_count"],
		"latest_notif_time": summary["latest_notif_time"],
	})
}

// GetNotificationDetail 获取通知详情（自动标记为已读）
func (h *NotificationHandler) GetNotificationDetail(c *gin.Context) {
	userID, exists := middleware.GetUserID(c)
	if !exists {
		utils.Unauthorized(c, "unauthorized")
		return
	}

	notificationID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.BadRequest(c, "invalid notification id")
		return
	}

	notification, err := h.notifSvc.GetNotificationDetail(userID, notificationID)
	if err != nil {
		utils.NotFound(c, err.Error())
		return
	}

	utils.SuccessResponse(c, gin.H{"notification": notification})
}

// MarkAllAsRead 标记所有通知为已读
func (h *NotificationHandler) MarkAllAsRead(c *gin.Context) {
	userID, exists := middleware.GetUserID(c)
	if !exists {
		utils.Unauthorized(c, "unauthorized")
		return
	}

	if err := h.notifSvc.MarkAllAsRead(userID); err != nil {
		utils.InternalServerError(c, err.Error())
		return
	}

	utils.SuccessWithMessage(c, "all notifications marked as read", nil)
}

// DeleteNotification 删除通知
func (h *NotificationHandler) DeleteNotification(c *gin.Context) {
	userID, exists := middleware.GetUserID(c)
	if !exists {
		utils.Unauthorized(c, "unauthorized")
		return
	}

	notificationID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.BadRequest(c, "invalid notification id")
		return
	}

	if err := h.notifSvc.DeleteNotification(userID, notificationID); err != nil {
		utils.NotFound(c, err.Error())
		return
	}

	utils.SuccessWithMessage(c, "notification deleted", nil)
}

// BatchSendNotification 批量发送通知（管理后台使用，使用模板）
func (h *NotificationHandler) BatchSendNotification(c *gin.Context) {
	userID, exists := middleware.GetUserID(c)
	if !exists {
		utils.Unauthorized(c, "unauthorized")
		return
	}

	// TODO: 这里应该验证用户是否是管理员
	_ = userID

	var req struct {
		UserIDs      []string               `json:"user_ids"`                         // 为空表示发送给所有用户
		TemplateType string                 `json:"template_type" binding:"required"` // 模板类型
		TemplateVars map[string]string      `json:"template_vars" binding:"required"` // 模板变量
		Metadata     map[string]interface{} `json:"metadata"`                         // 元数据（可选）
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}

	// 转换 user_ids 为 UUID
	var userIDs []uuid.UUID
	for _, idStr := range req.UserIDs {
		id, err := uuid.Parse(idStr)
		if err != nil {
			utils.BadRequest(c, "invalid user id: "+idStr)
			return
		}
		userIDs = append(userIDs, id)
	}

	// 使用模板批量发送通知
	successCount, err := h.notifSvc.SendNotificationWithTemplate(userIDs, req.TemplateType, req.TemplateVars, req.Metadata)
	if err != nil {
		utils.InternalServerError(c, err.Error())
		return
	}

	utils.SuccessResponse(c, gin.H{
		"success_count": successCount,
		"total_count":   len(userIDs),
		"message":       "notifications sent",
	})
}

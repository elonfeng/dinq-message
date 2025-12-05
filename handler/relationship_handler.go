package handler

import (
	"dinq_message/middleware"
	"dinq_message/service"
	"dinq_message/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type RelationshipHandler struct {
	relSvc *service.RelationshipService
}

func NewRelationshipHandler(relSvc *service.RelationshipService) *RelationshipHandler {
	return &RelationshipHandler{relSvc: relSvc}
}

// BlockUser 拉黑用户
func (h *RelationshipHandler) BlockUser(c *gin.Context) {
	userID, exists := middleware.GetUserID(c)
	if !exists {
		utils.Unauthorized(c, "unauthorized")
		return
	}

	var req struct {
		TargetUserID uuid.UUID `json:"target_user_id" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}

	// 不能拉黑自己
	if userID == req.TargetUserID {
		utils.BadRequest(c, "cannot block yourself")
		return
	}

	if err := h.relSvc.BlockUser(userID, req.TargetUserID); err != nil {
		utils.Conflict(c, err.Error())
		return
	}

	utils.SuccessWithMessage(c, "user blocked successfully", nil)
}

// UnblockUser 取消拉黑
func (h *RelationshipHandler) UnblockUser(c *gin.Context) {
	userID, exists := middleware.GetUserID(c)
	if !exists {
		utils.Unauthorized(c, "unauthorized")
		return
	}

	var req struct {
		TargetUserID uuid.UUID `json:"target_user_id" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}

	if err := h.relSvc.UnblockUser(userID, req.TargetUserID); err != nil {
		utils.NotFound(c, err.Error())
		return
	}

	utils.SuccessWithMessage(c, "user unblocked successfully", nil)
}

// GetBlockedUsers 获取拉黑列表
func (h *RelationshipHandler) GetBlockedUsers(c *gin.Context) {
	userID, exists := middleware.GetUserID(c)
	if !exists {
		utils.Unauthorized(c, "unauthorized")
		return
	}

	blockedUsers, err := h.relSvc.GetBlockedUsers(userID)
	if err != nil {
		utils.InternalServerError(c, err.Error())
		return
	}

	utils.SuccessResponse(c, gin.H{"blocked_users": blockedUsers})
}

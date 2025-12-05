package handler

import (
	"dinq_message/middleware"
	"dinq_message/service"
	"dinq_message/utils"

	"github.com/gin-gonic/gin"
)

type SystemSettingsHandler struct {
	sysSvc *service.SystemSettingsService
}

func NewSystemSettingsHandler(sysSvc *service.SystemSettingsService) *SystemSettingsHandler {
	return &SystemSettingsHandler{
		sysSvc: sysSvc,
	}
}

// GetSystemSettings 获取所有系统配置
// GET /api/admin/settings
func (h *SystemSettingsHandler) GetSystemSettings(c *gin.Context) {
	settings, err := h.sysSvc.GetAllSettings()
	if err != nil {
		utils.InternalServerError(c, err.Error())
		return
	}

	utils.SuccessResponse(c, gin.H{
		"settings": settings,
	})
}

// UpdateSystemSetting 更新系统配置
// PUT /api/admin/settings/:key
func (h *SystemSettingsHandler) UpdateSystemSetting(c *gin.Context) {
	key := c.Param("key")

	var req struct {
		Value string `json:"value" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "invalid request body")
		return
	}

	// 验证配置值（只允许 "true" 或 "false"）
	if req.Value != "true" && req.Value != "false" {
		utils.BadRequest(c, "value must be 'true' or 'false'")
		return
	}

	if err := h.sysSvc.UpdateSetting(key, req.Value); err != nil {
		utils.BadRequest(c, err.Error())
		return
	}

	utils.SuccessResponse(c, gin.H{
		"message": "setting updated successfully",
		"key":     key,
		"value":   req.Value,
	})
}

// ReloadSystemSettings 重新加载系统配置（从数据库）
// POST /api/admin/settings/reload
func (h *SystemSettingsHandler) ReloadSystemSettings(c *gin.Context) {
	if err := h.sysSvc.LoadSettings(); err != nil {
		utils.InternalServerError(c, err.Error())
		return
	}

	utils.SuccessResponse(c, gin.H{
		"message": "settings reloaded successfully",
	})
}

// AdminAuthMiddleware 超管鉴权中间件（简化版，实际应该检查用户角色）
func AdminAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// TODO: 实际生产环境应该检查用户是否是超级管理员
		// 目前简化处理，只要通过 JWT 认证即可
		userID, exists := middleware.GetUserID(c)
		if !exists {
			utils.Unauthorized(c, "unauthorized")
			c.Abort()
			return
		}

		// TODO: 查询数据库检查 userID 是否是管理员
		// 这里简化处理，假设所有认证用户都是管理员
		_ = userID

		c.Next()
	}
}

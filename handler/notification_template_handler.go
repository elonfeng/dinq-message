package handler

import (
	"dinq_message/model"
	"dinq_message/service"
	"dinq_message/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type NotificationTemplateHandler struct {
	templateSvc *service.NotificationTemplateService
}

func NewNotificationTemplateHandler(templateSvc *service.NotificationTemplateService) *NotificationTemplateHandler {
	return &NotificationTemplateHandler{
		templateSvc: templateSvc,
	}
}

// ListTemplates 获取所有通知模板
func (h *NotificationTemplateHandler) ListTemplates(c *gin.Context) {
	templates, err := h.templateSvc.ListTemplates()
	if err != nil {
		utils.InternalServerError(c, err.Error())
		return
	}

	utils.SuccessResponse(c, gin.H{"templates": templates})
}

// CreateTemplate 创建通知模板
func (h *NotificationTemplateHandler) CreateTemplate(c *gin.Context) {
	var req model.NotificationTemplate
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "Invalid request")
		return
	}

	template, err := h.templateSvc.CreateTemplate(&req)
	if err != nil {
		utils.InternalServerError(c, err.Error())
		return
	}

	utils.SuccessResponse(c, gin.H{"template": template})
}

// UpdateTemplate 更新通知模板
func (h *NotificationTemplateHandler) UpdateTemplate(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		utils.BadRequest(c, "Invalid template ID")
		return
	}

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		utils.BadRequest(c, "Invalid request")
		return
	}

	if err := h.templateSvc.UpdateTemplate(id, updates); err != nil {
		utils.InternalServerError(c, err.Error())
		return
	}

	utils.SuccessWithMessage(c, "Template updated successfully", nil)
}

// DeleteTemplate 删除通知模板
func (h *NotificationTemplateHandler) DeleteTemplate(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		utils.BadRequest(c, "Invalid template ID")
		return
	}

	if err := h.templateSvc.DeleteTemplate(id); err != nil {
		utils.InternalServerError(c, err.Error())
		return
	}

	utils.SuccessWithMessage(c, "Template deleted successfully", nil)
}

// InitDefaultTemplates 初始化默认模板
func (h *NotificationTemplateHandler) InitDefaultTemplates(c *gin.Context) {
	if err := h.templateSvc.InitDefaultTemplates(); err != nil {
		utils.InternalServerError(c, err.Error())
		return
	}

	utils.SuccessWithMessage(c, "Default templates initialized successfully", nil)
}

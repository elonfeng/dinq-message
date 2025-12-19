package main

import (
	"log"
	"time"

	"dinq_message/config"
	"dinq_message/handler"
	"dinq_message/middleware"
	"dinq_message/service"
	"dinq_message/utils"

	"github.com/gin-gonic/gin"
)

func init() {
	// 设置时区为 UTC（推荐服务端统一使用 UTC）
	time.Local = time.UTC
}

func main() {
	// 加载配置
	cfg := config.Load()

	// 初始化数据库
	if err := utils.InitDB(cfg.DatabaseURL); err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer utils.CloseDB()

	// 初始化 Redis
	if err := utils.InitRedis(cfg.RedisURL, cfg.RedisPassword, cfg.RedisDB); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	defer utils.CloseRedis()

	// 初始化认证中间件
	middleware.InitAuth(cfg.JWTSecret)

	// 创建系统配置服务（全局单例）
	sysSvc := service.NewSystemSettingsService(utils.GetDB())

	// 创建通知服务
	notifSvc := service.NewNotificationService(utils.GetDB())
	notifTemplateSvc := service.NewNotificationTemplateService(utils.GetDB())

	// 创建 WebSocket Hub（传入共享的 sysSvc 和配置）
	hub := handler.NewHubWithConfig(utils.GetDB(), utils.GetRedis(), sysSvc, cfg.MaxVideoSizeMB)

	// 设置通知服务的 Hub 通知器（用于WebSocket推送）
	notifSvc.SetHubNotifier(hub)

	// 获取 Hub 内部的 MessageService 并注入依赖
	hub.GetMessageService().SetNotificationService(notifSvc)
	hub.GetMessageService().SetHubChecker(hub)
	hub.GetMessageService().SetUnreadNotifier(hub)
	hub.GetMessageService().SetConversationNotifier(hub)

	// 创建服务
	convSvc := service.NewConversationServiceWithRedis(utils.GetDB(), utils.GetRedis())
	relSvc := service.NewRelationshipService(utils.GetDB())
	msgSvc := service.NewMessageServiceWithConfig(utils.GetDB(), utils.GetRedis(), sysSvc, cfg.MaxVideoSizeMB)

	// 为 msgSvc 也注入依赖（用于 HTTP API）
	msgSvc.SetNotificationService(notifSvc)
	msgSvc.SetHubChecker(hub)
	msgSvc.SetUnreadNotifier(hub)
	msgSvc.SetConversationNotifier(hub)

	// 创建处理器
	convHandler := handler.NewConversationHandler(convSvc)
	notifHandler := handler.NewNotificationHandler(notifSvc)
	notifTemplateHandler := handler.NewNotificationTemplateHandler(notifTemplateSvc)
	relHandler := handler.NewRelationshipHandler(relSvc)
	sysHandler := handler.NewSystemSettingsHandler(sysSvc)
	msgHandler := handler.NewMessageHandler(msgSvc, hub)

	// 初始化默认通知模板
	if err := notifTemplateSvc.InitDefaultTemplates(); err != nil {
		log.Printf("Warning: Failed to init default notification templates: %v", err)
	}

	// 创建 Gin 路由
	r := gin.Default()

	// 注册统一错误处理中间件
	r.Use(middleware.ErrorHandlerMiddleware())

	// 健康检查
	r.GET("/health", func(c *gin.Context) {
		utils.SuccessResponse(c, gin.H{"status": "ok"})
	})

	// WebSocket 连接（使用 token 认证，不需要 HTTP 中间件）
	r.GET("/ws", handler.HandleWebSocket(hub))

	// HTTP API 路由组（需要认证）
	api := r.Group("/api/v1")
	api.Use(middleware.AuthMiddleware())
	{
		// 会话管理
		api.GET("/conversations", convHandler.GetConversations)
		api.GET("/conversations/search", convHandler.SearchConversations)         // 搜索会话
		api.POST("/conversations/private", convHandler.CreatePrivateConversation) // 创建私聊会话
		api.POST("/conversations/group", convHandler.CreateGroup)                 // 创建群聊
		api.GET("/conversations/:id/messages", convHandler.GetMessages)           // 获取消息历史
		api.POST("/conversations/:id/hide", convHandler.HideConversation)         // 隐藏会话

		// 群聊成员管理
		api.POST("/conversations/:id/members", convHandler.AddMembers)
		api.POST("/conversations/:id/members/remove", convHandler.RemoveMember)
		api.POST("/conversations/:id/leave", convHandler.LeaveGroup)
		api.POST("/conversations/:id/members/:user_id/role", convHandler.UpdateMemberRole)

		// 消息管理
		api.POST("/messages/:id/recall", msgHandler.RecallMessage)
		api.GET("/messages/search", msgHandler.SearchMessages) // 搜索消息

		// 通知
		api.GET("/notifications", notifHandler.GetNotifications)
		api.GET("/notifications/:id", notifHandler.GetNotificationDetail)      // 查看通知详情（自动标记已读）
		api.POST("/notifications/read-all", notifHandler.MarkAllAsRead)        // 全部已读
		api.POST("/notifications/:id/delete", notifHandler.DeleteNotification) // 删除通知

		// 用户关系（拉黑）
		api.POST("/relationships/block", relHandler.BlockUser)
		api.POST("/relationships/unblock", relHandler.UnblockUser)
		api.GET("/relationships/blocked", relHandler.GetBlockedUsers)

		// 登出（清除在线状态）
		api.POST("/logout", func(c *gin.Context) {
			userID := c.GetHeader("X-User-ID")
			if userID != "" && sysSvc.IsFeatureEnabled("enable_online_status") {
				hub.ForceOffline(userID)
			}
			utils.SuccessWithMessage(c, "Logged out", nil)
		})
	}

	// 管理员 API 路由组（需要认证 + 管理员权限）
	admin := r.Group("/api/admin")
	admin.Use(middleware.AuthMiddleware())
	admin.Use(handler.AdminAuthMiddleware())
	{
		// 系统配置管理
		admin.GET("/settings", sysHandler.GetSystemSettings)
		admin.POST("/settings/:key", sysHandler.UpdateSystemSetting)
		admin.POST("/settings/reload", sysHandler.ReloadSystemSettings)

		// 通知模板管理
		admin.GET("/notification-templates", notifTemplateHandler.ListTemplates)
		admin.POST("/notification-templates", notifTemplateHandler.CreateTemplate)
		admin.POST("/notification-templates/:id", notifTemplateHandler.UpdateTemplate)
		admin.DELETE("/notification-templates/:id", notifTemplateHandler.DeleteTemplate)
		admin.POST("/notification-templates/init-defaults", notifTemplateHandler.InitDefaultTemplates)

		// 批量发送通知
		admin.POST("/notifications/batch-send", notifHandler.BatchSendNotification)
	}

	// 启动服务
	log.Printf("🚀 dinq_message service starting on port %s", cfg.Port)
	if err := r.Run(":" + cfg.Port); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

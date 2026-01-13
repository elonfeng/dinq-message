package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"dinq_message/middleware"
	"dinq_message/service"
	"dinq_message/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		// TODO: 生产环境需要检查 Origin
		return true
	},
}

// Client WebSocket 客户端
type Client struct {
	ID                    uuid.UUID
	UserID                uuid.UUID
	Conn                  *websocket.Conn
	Send                  chan []byte
	Hub                   *Hub
	CurrentConversationID *uuid.UUID // 用户当前正在查看的会话ID
	mu                    sync.RWMutex
	closed                bool // Send channel 是否已关闭
}

// Hub WebSocket 连接管理中心
type Hub struct {
	// 在线用户 map[userID]map[clientID]*Client（支持多设备）
	Clients map[uuid.UUID]map[uuid.UUID]*Client
	mu      sync.RWMutex

	// 最大连接数限制（每个用户）
	MaxConnectionsPerUser int

	// Redis 客户端
	rdb *redis.Client

	// 消息服务
	msgSvc *service.MessageService

	// 通知服务
	notifSvc *service.NotificationService

	// 系统配置服务
	sysSvc *service.SystemSettingsService

	// Pod ID（用于跨 Pod 广播去重）
	podID string

	// 停止 Pub/Sub 订阅
	stopPubSub chan struct{}
}

// Redis Pub/Sub channel 名称
const redisBroadcastChannel = "ws:broadcast"

// BroadcastMessage 跨 Pod 广播消息格式
type BroadcastMessage struct {
	UserID  string `json:"user_id"`
	PodID   string `json:"pod_id"` // 发送方 Pod ID，用于去重
	Payload []byte `json:"payload"`
}

// NewHub 创建 Hub
func NewHub(db *gorm.DB, rdb *redis.Client, sysSvc *service.SystemSettingsService) *Hub {
	return &Hub{
		Clients:               make(map[uuid.UUID]map[uuid.UUID]*Client),
		MaxConnectionsPerUser: 18, // 默认每个用户最多 18 个设备
		rdb:                   rdb,
		msgSvc:                service.NewMessageService(db, rdb, sysSvc),
		notifSvc:              service.NewNotificationService(db),
		sysSvc:                sysSvc,
		podID:                 uuid.New().String(), // 每个 Pod 实例唯一 ID
		stopPubSub:            make(chan struct{}),
	}
}

// NewHubWithConfig 创建 Hub（带配置）
func NewHubWithConfig(db *gorm.DB, rdb *redis.Client, sysSvc *service.SystemSettingsService, maxVideoSizeMB int) *Hub {
	return &Hub{
		Clients:               make(map[uuid.UUID]map[uuid.UUID]*Client),
		MaxConnectionsPerUser: 18, // 默认每个用户最多 18 个设备
		rdb:                   rdb,
		msgSvc:                service.NewMessageServiceWithConfig(db, rdb, sysSvc, maxVideoSizeMB),
		notifSvc:              service.NewNotificationService(db),
		sysSvc:                sysSvc,
		podID:                 uuid.New().String(), // 每个 Pod 实例唯一 ID
		stopPubSub:            make(chan struct{}),
	}
}

// Register 注册客户端（支持多设备，限制最大连接数）
func (h *Hub) Register(client *Client) {
	h.mu.Lock()

	// 初始化用户的连接 map
	if h.Clients[client.UserID] == nil {
		h.Clients[client.UserID] = make(map[uuid.UUID]*Client)
	}

	// 检查连接数限制
	if len(h.Clients[client.UserID]) >= h.MaxConnectionsPerUser {
		h.mu.Unlock() // 先释放锁，再进行网络操作

		log.Printf("[ERROR] User %s exceeds max connections (%d), rejecting new connection (client ID: %s)",
			client.UserID, h.MaxConnectionsPerUser, client.ID)

		// 先发送结构化错误消息，方便前端友好提示
		errPayload := map[string]interface{}{
			"type": "error",
			"data": map[string]interface{}{
				"code":    "too_many_devices",
				"message": fmt.Sprintf("Maximum %d devices allowed", h.MaxConnectionsPerUser),
			},
		}
		if msg, err := json.Marshal(errPayload); err == nil {
			_ = client.Conn.WriteMessage(websocket.TextMessage, msg)
		}

		// 拒绝连接（不持有锁的情况下进行网络操作）
		client.Conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure,
				fmt.Sprintf("Maximum %d devices allowed", h.MaxConnectionsPerUser)))
		client.Conn.Close()
		return
	}

	// 添加新连接
	h.Clients[client.UserID][client.ID] = client
	deviceCount := len(h.Clients[client.UserID])
	totalUsers := len(h.Clients)
	isFirstDevice := deviceCount == 1

	h.mu.Unlock() // 尽早释放锁

	// 在线状态处理（不持有锁的情况下进行 Redis 和通知操作）
	if h.sysSvc.IsFeatureEnabled("enable_online_status") {
		ctx := context.Background()
		h.rdb.Set(ctx, "online:"+client.UserID.String(), "1", 30*time.Second)

		// 仅第一个设备连接时推送上线通知
		if isFirstDevice {
			go h.notifyOnlineStatusChange(client.UserID, true)
		}
	}

	log.Printf("User %s connected (client: %s), total devices: %d, total users: %d",
		client.UserID, client.ID, deviceCount, totalUsers)
}

// Unregister 注销客户端（支持多设备）
func (h *Hub) Unregister(client *Client) {
	h.mu.Lock()

	// 检查用户的连接列表是否存在
	if userClients, exists := h.Clients[client.UserID]; exists {
		// 检查该 client 是否在列表中
		if _, found := userClients[client.ID]; found {
			// 删除该连接
			delete(userClients, client.ID)

			// 如果用户没有任何连接了，删除整个 userID 的 map
			if len(userClients) == 0 {
				delete(h.Clients, client.UserID)

				// 如果启用了在线状态功能，删除 Redis 在线状态并推送下线通知
				if h.sysSvc.IsFeatureEnabled("enable_online_status") {
					ctx := context.Background()
					h.rdb.Del(ctx, "online:"+client.UserID.String())

					// 推送下线通知给相关用户（最后一个设备断开时）
					go h.notifyOnlineStatusChange(client.UserID, false)
				}

				log.Printf("User %s disconnected (client: %s), all devices offline, total users: %d",
					client.UserID, client.ID, len(h.Clients))
			} else {
				log.Printf("User %s disconnected (client: %s), remaining devices: %d",
					client.UserID, client.ID, len(userClients))
			}
		}
	}

	h.mu.Unlock()

	// 安全关闭 Send channel
	client.mu.Lock()
	if !client.closed {
		close(client.Send)
		client.closed = true
	}
	client.mu.Unlock()
}

// SendToUser 发送消息给指定用户的所有设备
func (h *Hub) SendToUser(userID uuid.UUID, message []byte) bool {
	h.mu.RLock()
	userClients, exists := h.Clients[userID]
	if !exists || len(userClients) == 0 {
		h.mu.RUnlock()
		// 用户不在线（正常情况，不记录）
		return false
	}

	// 复制一份 client 列表，避免在遍历时发生并发修改 panic
	clientsCopy := make([]*Client, 0, len(userClients))
	for _, client := range userClients {
		clientsCopy = append(clientsCopy, client)
	}
	h.mu.RUnlock()

	// 发送给该用户的所有设备
	sentToAny := false
	for _, client := range clientsCopy {
		select {
		case client.Send <- message:
			sentToAny = true
		default:
			// 发送通道满了，关闭该设备连接
			log.Printf("[ERROR] Send channel FULL: user=%s, client=%s, closing connection", userID, client.ID)
			go h.Unregister(client)
		}
	}

	return sentToAny
}

// BroadcastToUser 广播消息给用户（支持跨 Pod）
// 先尝试本地发送，同时 publish 到 Redis 让其他 Pod 也能收到
func (h *Hub) BroadcastToUser(userID uuid.UUID, message []byte) {
	// 1. 先尝试本地发送
	h.SendToUser(userID, message)

	// 2. 发布到 Redis，让其他 Pod 也能推送
	broadcastMsg := BroadcastMessage{
		UserID:  userID.String(),
		PodID:   h.podID,
		Payload: message,
	}
	msgBytes, err := json.Marshal(broadcastMsg)
	if err != nil {
		log.Printf("[ERROR] Failed to marshal broadcast message: %v", err)
		return
	}

	ctx := context.Background()
	if err := h.rdb.Publish(ctx, redisBroadcastChannel, msgBytes).Err(); err != nil {
		log.Printf("[ERROR] Failed to publish to Redis: %v", err)
	}
}

// StartPubSub 启动 Redis Pub/Sub 订阅（跨 Pod 消息广播）
func (h *Hub) StartPubSub() {
	go func() {
		ctx := context.Background()
		pubsub := h.rdb.Subscribe(ctx, redisBroadcastChannel)
		defer pubsub.Close()

		log.Printf("[INFO] Pod %s started Redis Pub/Sub subscription", h.podID[:8])

		ch := pubsub.Channel()
		for {
			select {
			case <-h.stopPubSub:
				log.Printf("[INFO] Pod %s stopping Redis Pub/Sub subscription", h.podID[:8])
				return
			case msg := <-ch:
				if msg == nil {
					continue
				}
				h.handleBroadcastMessage([]byte(msg.Payload))
			}
		}
	}()
}

// StopPubSub 停止 Redis Pub/Sub 订阅
func (h *Hub) StopPubSub() {
	close(h.stopPubSub)
}

// handleBroadcastMessage 处理来自 Redis 的广播消息
func (h *Hub) handleBroadcastMessage(data []byte) {
	var msg BroadcastMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("[ERROR] Failed to unmarshal broadcast message: %v", err)
		return
	}

	// 忽略自己发的消息（避免重复推送）
	if msg.PodID == h.podID {
		return
	}

	// 推送给本地用户
	userID, err := uuid.Parse(msg.UserID)
	if err != nil {
		log.Printf("[ERROR] Invalid user ID in broadcast message: %v", err)
		return
	}

	h.SendToUser(userID, msg.Payload)
}

// GetMessageService 获取消息服务（用于依赖注入）
func (h *Hub) GetMessageService() *service.MessageService {
	return h.msgSvc
}

// IsOnline 检查用户是否在线（至少有一个设备在线）
func (h *Hub) IsOnline(userID uuid.UUID) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	userClients, exists := h.Clients[userID]
	return exists && len(userClients) > 0
}

// IsUserOnline 检查用户是否在线（至少有一个设备在线）
func (h *Hub) IsUserOnline(userID uuid.UUID) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	userClients, exists := h.Clients[userID]
	return exists && len(userClients) > 0
}

// SendNotification 通过 WebSocket 发送通知给用户
func (h *Hub) SendNotification(userID uuid.UUID, notification interface{}) bool {
	response := map[string]interface{}{
		"type": "notification",
		"data": notification,
	}
	responseData, err := json.Marshal(response)
	if err != nil {
		log.Printf("[ERROR] Failed to marshal notification: %v", err)
		return false
	}
	h.BroadcastToUser(userID, responseData)
	return true
}

// SendUnreadCountUpdate 推送未读数量更新
func (h *Hub) SendUnreadCountUpdate(userID uuid.UUID, conversationID uuid.UUID, unreadCount int) bool {
	response := map[string]interface{}{
		"type": "unread_count_update",
		"data": map[string]interface{}{
			"conversation_id": conversationID,
			"unread_count":    unreadCount,
		},
	}
	responseData, err := json.Marshal(response)
	if err != nil {
		log.Printf("[ERROR] Failed to marshal unread count update: %v", err)
		return false
	}
	h.BroadcastToUser(userID, responseData)
	return true
}

// SendOnlineStatusUpdate 推送在线状态变化
func (h *Hub) SendOnlineStatusUpdate(userID uuid.UUID, targetUserID uuid.UUID, isOnline bool) bool {
	response := map[string]interface{}{
		"type": "online_status_update",
		"data": map[string]interface{}{
			"user_id":   targetUserID,
			"is_online": isOnline,
		},
	}
	responseData, err := json.Marshal(response)
	if err != nil {
		log.Printf("[ERROR] Failed to marshal online status update: %v", err)
		return false
	}
	h.BroadcastToUser(userID, responseData)
	return true
}

// SendConversationUpdate 推送会话更新(包含最新消息时间等)
func (h *Hub) SendConversationUpdate(userID uuid.UUID, conversationID uuid.UUID, lastMessageTime *time.Time, lastMessageText *string, unreadCount int) bool {
	response := map[string]interface{}{
		"type": "conversation_update",
		"data": map[string]interface{}{
			"conversation_id":   conversationID,
			"last_message_time": lastMessageTime,
			"last_message_text": lastMessageText,
			"unread_count":      unreadCount,
		},
	}
	responseData, err := json.Marshal(response)
	if err != nil {
		log.Printf("[ERROR] ConvUpdate marshal failed: user=%s, conversation=%s, error=%v", userID, conversationID, err)
		return false
	}

	h.BroadcastToUser(userID, responseData)
	return true
}

// SendNotificationUpdate 推送通知更新(未读数量+最新通知时间)
func (h *Hub) SendNotificationUpdate(userID uuid.UUID, unreadCount int, latestNotifTime *time.Time) bool {
	response := map[string]interface{}{
		"type": "notification_update",
		"data": map[string]interface{}{
			"unread_count":      unreadCount,
			"latest_notif_time": latestNotifTime,
		},
	}
	responseData, err := json.Marshal(response)
	if err != nil {
		log.Printf("[ERROR] Failed to marshal notification update: %v", err)
		return false
	}
	h.BroadcastToUser(userID, responseData)
	return true
}

// notifyOnlineStatusChange 通知相关用户在线状态变化
func (h *Hub) notifyOnlineStatusChange(userID uuid.UUID, isOnline bool) {
	// 查询该用户的所有私聊会话
	var conversations []struct {
		ConversationID   uuid.UUID `gorm:"column:conversation_id"`
		OtherUserID      uuid.UUID `gorm:"column:other_user_id"`
		ConversationType string    `gorm:"column:conversation_type"`
	}

	// 查询该用户参与的私聊会话及对方用户ID
	// 优化：先从 conversation_members 查找，减少 JOIN 开销
	err := h.msgSvc.GetDB().Raw(`
		SELECT
			cm1.conversation_id,
			cm2.user_id as other_user_id,
			'private' as conversation_type
		FROM conversation_members cm1
		INNER JOIN conversation_members cm2
			ON cm1.conversation_id = cm2.conversation_id
			AND cm2.user_id != ?
		INNER JOIN conversations c
			ON c.id = cm1.conversation_id
			AND c.conversation_type = 'private'
		WHERE cm1.user_id = ?
	`, userID, userID).Scan(&conversations).Error

	if err != nil {
		log.Printf("[ERROR] Failed to query conversations for online status update: %v", err)
		return
	}

	// 推送给每个相关的在线用户
	for _, conv := range conversations {
		h.SendOnlineStatusUpdate(conv.OtherUserID, userID, isOnline)
	}
}

// ForceOffline 强制用户离线（用于登出）
func (h *Hub) ForceOffline(userIDStr string) {
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return
	}

	// 删除 Redis 在线状态
	if h.sysSvc.IsFeatureEnabled("enable_online_status") {
		ctx := context.Background()
		h.rdb.Del(ctx, "online:"+userID.String())
	}

	// 断开用户的所有 WebSocket 连接
	h.mu.RLock()
	userClients, exists := h.Clients[userID]
	if exists {
		// 复制一份 client 列表，避免在遍历时修改
		clientsCopy := make([]*Client, 0, len(userClients))
		for _, client := range userClients {
			clientsCopy = append(clientsCopy, client)
		}
		h.mu.RUnlock()

		// 注销所有设备
		for _, client := range clientsCopy {
			h.Unregister(client)
		}
	} else {
		h.mu.RUnlock()
	}
}

// IsUserInConversation 检查用户是否正在查看指定会话（多设备支持）
// 只要用户的任意一个设备在查看该会话，就返回 true
func (h *Hub) IsUserInConversation(userID uuid.UUID, conversationID uuid.UUID) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	userClients, exists := h.Clients[userID]
	if !exists || len(userClients) == 0 {
		return false // 用户不在线
	}

	// 检查任意一个设备是否正在查看指定会话
	for _, client := range userClients {
		client.mu.RLock()
		isViewing := client.CurrentConversationID != nil && *client.CurrentConversationID == conversationID
		client.mu.RUnlock()

		if isViewing {
			return true // 有设备在查看该会话
		}
	}

	return false // 所有设备都没在查看该会话
}

// WSMessage WebSocket 消息格式
type WSMessage struct {
	Type string          `json:"type"` // 'message' | 'typing' | 'read' | 'heartbeat'
	Data json.RawMessage `json:"data"`
}

// HandleWebSocket 处理 WebSocket 连接
func HandleWebSocket(hub *Hub) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 从 query 参数获取 token
		tokenString := c.Query("token")
		if tokenString == "" {
			utils.Unauthorized(c, "missing token")
			return
		}

		// 验证 token
		userID, err := middleware.ValidateToken(tokenString)
		if err != nil {
			utils.Unauthorized(c, "invalid token")
			return
		}

		// 升级为 WebSocket 连接
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			log.Printf("[ERROR] WebSocket upgrade failed for user %s: %v", userID, err)
			return
		}

		// 创建客户端
		client := &Client{
			ID:     uuid.New(),
			UserID: userID,
			Conn:   conn,
			Send:   make(chan []byte, 1024), // 增加缓冲区，应对高并发场景
			Hub:    hub,
		}

		// 注册客户端
		hub.Register(client)

		// 发送离线消息
		go client.sendOfflineMessages()

		// 启动读写协程
		go client.readPump()
		go client.writePump()
	}
}

// readPump 从 WebSocket 读取消息
func (c *Client) readPump() {
	defer func() {
		c.Hub.Unregister(c)
		c.Conn.Close()
	}()

	c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived, websocket.CloseAbnormalClosure) {
				log.Printf("[ERROR] User %s WebSocket unexpected close error: %v", c.UserID, err)
			}
			break
		}

		// 解析消息
		var wsMsg WSMessage
		if err := json.Unmarshal(message, &wsMsg); err != nil {
			log.Printf("[ERROR] Invalid message format: %v", err)
			// 发送错误消息给客户端
			errorResponse := map[string]interface{}{
				"type": "error",
				"data": map[string]interface{}{
					"message": "Invalid JSON format",
				},
			}
			if responseData, err := json.Marshal(errorResponse); err == nil {
				c.Send <- responseData
			}
			continue
		}

		// 处理不同类型的消息
		switch wsMsg.Type {
		case "heartbeat":
			// 心跳消息，如果启用了在线状态功能，刷新 Redis
			if c.Hub.sysSvc.IsFeatureEnabled("enable_online_status") {
				ctx := context.Background()
				c.Hub.rdb.Set(ctx, "online:"+c.UserID.String(), "1", 30*time.Second)
			}

		case "message":
			// 聊天消息
			c.handleSendMessage(wsMsg.Data)

		case "typing":
			// 正在输入提示
			c.handleTyping(wsMsg.Data)

		case "read":
			// 已读回执
			c.handleMarkAsRead(wsMsg.Data)

		case "recall":
			// 撤回消息
			c.handleRecallMessage(wsMsg.Data)

		case "set_current_conversation":
			// 设置当前正在查看的会话（用于智能通知）
			c.handleSetCurrentConversation(wsMsg.Data)
		}
	}
}

// writePump 向 WebSocket 写入消息
func (c *Client) writePump() {
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				// Hub 关闭了通道
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.Conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			// 发送 ping 保持连接
			c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// handleSendMessage 处理发送消息
func (c *Client) handleSendMessage(data json.RawMessage) {
	var req service.SendMessageRequest
	if err := json.Unmarshal(data, &req); err != nil {
		log.Printf("[ERROR] Invalid message format: %v", err)
		c.sendError("Invalid message format")
		return
	}

	// 发送消息
	message, err := c.Hub.msgSvc.SendMessage(c.UserID, &req)
	if err != nil {
		log.Printf("[ERROR] Failed to send message: %v", err)
		c.sendError(err.Error())
		return
	}

	// 获取会话中的所有在线成员
	members, err := c.Hub.msgSvc.GetConversationMembers(message.ConversationID)
	if err != nil {
		log.Printf("[ERROR] Failed to get conversation members: %v", err)
		members = []uuid.UUID{} // 空数组，避免后续panic
	}

	// 为每个成员计算 can_send 状态并发送消息
	for _, memberID := range members {
		// 计算该成员是否可以发送消息
		canSend := c.Hub.msgSvc.CheckCanSend(memberID, message.ConversationID)

		// 解析 metadata (json.RawMessage -> map)
		var metadata map[string]interface{}
		if len(message.Metadata) > 0 {
			json.Unmarshal(message.Metadata, &metadata)
		}

		// 构造包含 can_send 的响应
		response := map[string]interface{}{
			"type": "message",
			"data": map[string]interface{}{
				"id":                  message.ID,
				"conversation_id":     message.ConversationID,
				"sender_id":           message.SenderID,
				"message_type":        message.MessageType,
				"content":             message.Content,
				"metadata":            metadata,
				"status":              message.Status,
				"created_at":          message.CreatedAt,
				"reply_to_message_id": message.ReplyToMessageID, // 回复消息ID
				"can_send":            canSend,                  // 告诉前端是否可以发送
			},
		}
		responseData, _ := json.Marshal(response)
		c.Hub.BroadcastToUser(memberID, responseData)

		// 注意：会话更新推送已经在 message_service.SendMessage() 中完成
		// 不需要在这里重复推送，避免竞态条件和重复查询数据库
	}
}

// handleTyping 处理正在输入提示
func (c *Client) handleTyping(data json.RawMessage) {
	// 检查系统是否启用了正在输入提示功能
	if !c.Hub.sysSvc.IsFeatureEnabled("enable_typing_indicator") {
		return
	}

	var req struct {
		ConversationID uuid.UUID `json:"conversation_id"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return
	}

	// 广播给会话中的其他在线成员
	response := map[string]interface{}{
		"type": "typing",
		"data": map[string]interface{}{
			"conversation_id": req.ConversationID,
			"user_id":         c.UserID,
		},
	}
	responseData, _ := json.Marshal(response)

	members, _ := c.Hub.msgSvc.GetConversationMembers(req.ConversationID)
	for _, memberID := range members {
		if memberID != c.UserID {
			c.Hub.BroadcastToUser(memberID, responseData)
		}
	}
}

// handleMarkAsRead 处理已读回执
func (c *Client) handleMarkAsRead(data json.RawMessage) {
	var req struct {
		ConversationID uuid.UUID `json:"conversation_id"`
		MessageID      uuid.UUID `json:"message_id"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		log.Printf("[ERROR] Invalid read receipt format: %v", err)
		return
	}

	// 标记为已读（这个操作总是执行，更新未读计数）
	if err := c.Hub.msgSvc.MarkAsRead(c.UserID, req.ConversationID, req.MessageID); err != nil {
		log.Printf("[ERROR] Failed to mark as read: %v", err)
		return
	}

	// 如果启用了已读回执功能，广播已读状态给其他成员
	if c.Hub.sysSvc.IsFeatureEnabled("enable_read_receipt") {
		response := map[string]interface{}{
			"type": "read",
			"data": map[string]interface{}{
				"conversation_id": req.ConversationID,
				"message_id":      req.MessageID,
				"reader_id":       c.UserID,
			},
		}
		responseData, _ := json.Marshal(response)

		members, _ := c.Hub.msgSvc.GetConversationMembers(req.ConversationID)
		for _, memberID := range members {
			if memberID != c.UserID {
				c.Hub.BroadcastToUser(memberID, responseData)
			}
		}
	}
}

// handleRecallMessage 处理撤回消息
func (c *Client) handleRecallMessage(data json.RawMessage) {
	var req struct {
		MessageID uuid.UUID `json:"message_id"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		log.Printf("[ERROR] Invalid recall format: %v", err)
		c.sendError("Invalid recall format")
		return
	}

	// 先查询消息获取conversation_id
	message, err := c.Hub.msgSvc.GetMessageByID(req.MessageID)
	if err != nil {
		log.Printf("[ERROR] Message not found: %v", err)
		c.sendError("Message not found")
		return
	}

	// 撤回消息
	if err := c.Hub.msgSvc.RecallMessage(c.UserID, req.MessageID); err != nil {
		log.Printf("[ERROR] Failed to recall message: %v", err)
		c.sendError(err.Error())
		return
	}

	// 广播撤回通知给会话中的所有在线成员
	response := map[string]interface{}{
		"type": "recalled",
		"data": map[string]interface{}{
			"message_id": req.MessageID,
		},
	}
	responseData, _ := json.Marshal(response)

	// 发送给会话中所有成员
	members, err := c.Hub.msgSvc.GetConversationMembers(message.ConversationID)
	if err == nil {
		for _, memberID := range members {
			c.Hub.BroadcastToUser(memberID, responseData)
		}
	}
}

// handleSetCurrentConversation 设置用户当前正在查看的会话
func (c *Client) handleSetCurrentConversation(data json.RawMessage) {
	var req struct {
		ConversationID *string `json:"conversation_id"` // null表示离开聊天页面
	}
	if err := json.Unmarshal(data, &req); err != nil {
		log.Printf("[ERROR] Invalid set_current_conversation format: %v", err)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if req.ConversationID == nil || *req.ConversationID == "" {
		// 用户离开聊天页面
		c.CurrentConversationID = nil
	} else {
		// 用户进入特定会话页面
		convID, err := uuid.Parse(*req.ConversationID)
		if err != nil {
			log.Printf("[ERROR] Invalid conversation_id: %v", err)
			return
		}
		c.CurrentConversationID = &convID
	}
}

// sendOfflineMessages 发送离线消息给客户端
func (c *Client) sendOfflineMessages() {
	ctx := context.Background()
	key := "offline_msg:" + c.UserID.String()

	// 从Redis获取所有离线消息
	messages, err := c.Hub.rdb.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		log.Printf("[ERROR] Failed to get offline messages for user %s: %v", c.UserID, err)
		return
	}

	if len(messages) == 0 {
		return
	}

	// 发送每条离线消息
	for _, msgData := range messages {
		var message map[string]interface{}
		if err := json.Unmarshal([]byte(msgData), &message); err != nil {
			log.Printf("[ERROR] Failed to unmarshal offline message: %v", err)
			continue
		}

		// 发送离线消息（type: offline_message）
		response := map[string]interface{}{
			"type": "offline_message",
			"data": message,
		}
		responseData, _ := json.Marshal(response)

		// 非阻塞发送，避免 channel 满时阻塞
		select {
		case c.Send <- responseData:
			// 发送成功
		default:
			// channel 满了，跳过这条消息
			log.Printf("[ERROR] Failed to send offline message to user %s: channel full", c.UserID)
		}
	}

	// 删除已发送的离线消息
	c.Hub.rdb.Del(ctx, key)

	// 推送最新一条未读通知
	if c.Hub.notifSvc != nil {
		latestNotif, err := c.Hub.notifSvc.GetLatestUnreadNotification(c.UserID)
		if err != nil {
			log.Printf("[ERROR] Failed to get latest unread notification for user %s: %v", c.UserID, err)
		} else if latestNotif != nil {
			// 发送通知
			response := map[string]interface{}{
				"type": "notification",
				"data": latestNotif,
			}
			responseData, _ := json.Marshal(response)

			// 非阻塞发送
			select {
			case c.Send <- responseData:
				// 发送成功
			default:
				log.Printf("[ERROR] Failed to send notification to user %s: channel full", c.UserID)
			}
		}
	}
}

// sendError 发送错误消息给客户端
func (c *Client) sendError(errMsg string) {
	response := map[string]interface{}{
		"type": "error",
		"data": map[string]string{
			"message": errMsg,
		},
	}
	responseData, _ := json.Marshal(response)

	// 非阻塞发送
	select {
	case c.Send <- responseData:
		// 发送成功
	default:
		log.Printf("[ERROR] Failed to send error message to user %s: channel full", c.UserID)
	}
}

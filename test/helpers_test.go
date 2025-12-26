package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

// 测试配置
var (
	BaseURL   = "http://localhost:8083"
	WSURL     = "ws://localhost:8083"
	APIPrefix = "/api/v1"                        // API 路由前缀
	JWTSecret = "your-super-secret-jwt-key-here" // ⚠️ 改成测试环境的 JWT_SECRET

	// Redis 配置（和 .env 保持一致）
	RedisURL      = "localhost:6379"
	RedisPassword = ""
	RedisDB       = 0
)

// getRedisClient 获取 Redis 客户端
func getRedisClient() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:     RedisURL,
		Password: RedisPassword,
		DB:       RedisDB,
	})
}

// TestUser 测试用户
type TestUser struct {
	ID    uuid.UUID
	Token string
}

// generateJWT 生成 JWT Token
func generateJWT(userID uuid.UUID) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": userID.String(),
		"exp":     time.Now().Add(24 * time.Hour).Unix(),
	})
	tokenString, _ := token.SignedString([]byte(JWTSecret))
	return tokenString
}

// createTestUser 创建测试用户
func createTestUser() *TestUser {
	userID := uuid.New()
	return &TestUser{
		ID:    userID,
		Token: generateJWT(userID),
	}
}

// httpRequest HTTP 请求辅助函数
func httpRequest(method, path, token string, body interface{}) (*http.Response, []byte, error) {
	var bodyReader io.Reader
	if body != nil {
		jsonData, _ := json.Marshal(body)
		bodyReader = bytes.NewBuffer(jsonData)
	}

	req, err := http.NewRequest(method, BaseURL+path, bodyReader)
	if err != nil {
		return nil, nil, err
	}

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}

	respBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, respBody, err
}

// connectWebSocket WebSocket 连接辅助函数
func connectWebSocket(token string) (*websocket.Conn, error) {
	url := fmt.Sprintf("%s/ws?token=%s", WSURL, token)
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	return conn, err
}

// wsSend WebSocket 发送消息
func wsSend(conn *websocket.Conn, msgType string, data interface{}) error {
	msg := map[string]interface{}{
		"type": msgType,
		"data": data,
	}
	return conn.WriteJSON(msg)
}

// wsReceiveRaw 原始接收 WebSocket 消息（不跳过任何消息）
// 用于需要测试 unread_count_update、conversation_update 等系统推送消息的测试
func wsReceiveRaw(conn *websocket.Conn, timeout time.Duration) (map[string]interface{}, error) {
	conn.SetReadDeadline(time.Now().Add(timeout))
	var msg map[string]interface{}
	err := conn.ReadJSON(&msg)
	return msg, err
}

// wsReceive WebSocket 接收消息（带超时）
// 默认行为：自动跳过系统推送的更新消息（unread_count_update, conversation_update）
// 返回有意义的消息（message, error, typing, read, notification 等）
// 如果需要接收更新消息，请使用 wsReceiveRaw
func wsReceive(conn *websocket.Conn, timeout time.Duration) (map[string]interface{}, error) {
	maxAttempts := 10 // 最多尝试10次
	for i := 0; i < maxAttempts; i++ {
		msg, err := wsReceiveRaw(conn, timeout)
		if err != nil {
			return nil, err
		}

		// 自动跳过系统推送的计数/会话更新消息
		msgType, ok := msg["type"].(string)
		if !ok {
			return msg, nil // 如果没有 type 字段，直接返回
		}

		if msgType != "unread_count_update" && msgType != "conversation_update" {
			return msg, nil // 返回非更新类型的消息
		}
		// 继续循环，接收下一条消息
	}
	return nil, fmt.Errorf("did not receive non-update message after %d attempts", maxAttempts)
}

// wsReceiveMessageType 接收指定类型的 WebSocket 消息
// 跳过其他类型的消息，最多尝试 maxAttempts 次
func wsReceiveMessageType(conn *websocket.Conn, msgType string, timeout time.Duration, maxAttempts int) (map[string]interface{}, error) {
	for i := 0; i < maxAttempts; i++ {
		msg, err := wsReceiveRaw(conn, timeout)
		if err != nil {
			return nil, err
		}
		if msg["type"] == msgType {
			return msg, nil
		}
	}
	return nil, fmt.Errorf("did not receive message type '%s' after %d attempts", msgType, maxAttempts)
}

// parseResponse 解析 HTTP 响应为 map（统一响应格式）
func parseResponse(body []byte) map[string]interface{} {
	var response struct {
		Code    int                    `json:"code"`
		Message string                 `json:"message"`
		Data    map[string]interface{} `json:"data"`
	}
	json.Unmarshal(body, &response)
	// 返回 data 字段，保持向后兼容
	if response.Data != nil {
		return response.Data
	}
	// 如果没有 data 字段，返回整个响应（用于错误情况）
	var result map[string]interface{}
	json.Unmarshal(body, &result)
	return result
}

// getConversationList 获取会话列表
func getConversationList(token string) ([]interface{}, error) {
	resp, body, err := httpRequest("GET", APIPrefix+"/conversations?limit=50", token, nil)
	if err != nil || resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to get conversation list")
	}

	result := parseResponse(body)
	conversations, ok := result["conversations"].([]interface{})
	if !ok {
		return []interface{}{}, nil
	}
	return conversations, nil
}

// getMessages 获取会话消息列表
func getMessages(token, conversationID string) ([]interface{}, error) {
	resp, body, err := httpRequest("GET", APIPrefix+"/conversations/"+conversationID+"/messages?limit=50", token, nil)
	if err != nil || resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to get messages")
	}

	result := parseResponse(body)
	messages, ok := result["messages"].([]interface{})
	if !ok {
		return []interface{}{}, nil
	}
	return messages, nil
}

// findConversationByID 从会话列表中查找指定ID的会话
func findConversationByID(conversations []interface{}, convID string) map[string]interface{} {
	for _, conv := range conversations {
		c := conv.(map[string]interface{})
		if c["id"].(string) == convID {
			return c
		}
	}
	return nil
}

// findMessageByID 从消息列表中查找指定ID的消息
func findMessageByID(messages []interface{}, msgID string) map[string]interface{} {
	for _, msg := range messages {
		m := msg.(map[string]interface{})
		if m["id"].(string) == msgID {
			return m
		}
	}
	return nil
}

// getMemberUnreadCount 获取指定用户在会话中的未读数
func getMemberUnreadCount(conversation map[string]interface{}, userID string) int {
	members, ok := conversation["members"].([]interface{})
	if !ok {
		return -1
	}

	for _, m := range members {
		member := m.(map[string]interface{})
		if member["user_id"].(string) == userID {
			return int(member["unread_count"].(float64))
		}
	}
	return -1
}

// getMemberRole 获取指定用户在会话中的角色
func getMemberRole(conversation map[string]interface{}, userID string) string {
	members, ok := conversation["members"].([]interface{})
	if !ok {
		return ""
	}

	for _, m := range members {
		member := m.(map[string]interface{})
		if member["user_id"].(string) == userID {
			return member["role"].(string)
		}
	}
	return ""
}

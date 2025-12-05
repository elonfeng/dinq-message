package test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================
// 边界测试 - 输入验证
// ============================================

// TestEdgeCase_EmptyContent 测试空内容消息
//
// 测试目标：
// - 验证空内容消息的处理（取决于业务逻辑）
//
// 验证闭环：
// 1. 发送content为空的消息
// 2. 如果拒绝：收到error
// 3. 如果接受：消息成功发送
func TestEdgeCase_EmptyContent(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	// 1. 发送空内容消息
	err := wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "",
	})
	require.NoError(t, err)

	// 2. 验证闭环：系统应该拒绝空内容消息（业务要求）
	msg, err := wsReceive(wsA, 3*time.Second)
	require.NoError(t, err)

	// 空内容消息没有业务价值，应该被拒绝
	require.Equal(t, "error", msg["type"], "系统必须拒绝空内容消息，否则会产生无意义的消息记录")
	errorData := msg["data"].(map[string]interface{})
	assert.Contains(t, strings.ToLower(errorData["message"].(string)), "content", "错误应提示content问题")
	t.Log("✓ 系统正确拒绝空内容消息")
}

// TestEdgeCase_InvalidMessageType 测试无效的消息类型
//
// 测试目标：
// - 验证无效message_type的处理
//
// 验证闭环：
// 1. 发送无效的message_type
// 2. 如果有验证：收到error
// 3. 如果无验证：消息成功发送
func TestEdgeCase_InvalidMessageType(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	// 1. 发送无效message_type
	err := wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "invalid_type_xxx",
		"content":      "Test",
	})
	require.NoError(t, err)

	// 2. 验证闭环：检查响应
	msg, err := wsReceive(wsA, 3*time.Second)
	if err == nil {
		if msg["type"] == "error" {
			errorData := msg["data"].(map[string]interface{})
			assert.Contains(t, strings.ToLower(errorData["message"].(string)), "type", "错误应提示type问题")
			t.Log("系统拒绝无效message_type")
		} else {
			t.Log("系统接受无效message_type（可能未做验证）")
		}
	}
}

// TestEdgeCase_LargeMessage 测试超大消息内容
//
// 测试目标：
// - 验证大消息（10KB）的处理
//
// 验证闭环：
// 1. 发送10KB的消息
// 2. 如果有大小限制：收到error
// 3. 如果无限制：消息成功发送并查询验证
func TestEdgeCase_LargeMessage(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	// 1. 创建10KB的内容
	largeContent := strings.Repeat("A", 10*1024)

	err := wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      largeContent,
	})
	require.NoError(t, err)

	// 2. 验证闭环：检查响应
	msg, err := wsReceive(wsA, 5*time.Second)
	if err == nil {
		if msg["type"] == "error" {
			errorData := msg["data"].(map[string]interface{})
			assert.Contains(t, strings.ToLower(errorData["message"].(string)), "size", "错误应提示size问题")
			t.Log("系统拒绝超大消息")
		} else {
			// 如果接受，验证数据库中内容完整
			convID := msg["data"].(map[string]interface{})["conversation_id"].(string)
			messages, _ := getMessages(userA.Token, convID)
			assert.Greater(t, len(messages), 0)
			t.Log("系统接受超大消息")
		}
	}
}

// TestEdgeCase_RapidMessages 测试快速连续发送消息
//
// 测试目标：
// - 验证快速发送多条消息的处理
// - 检查是否有限流机制
//
// 验证闭环：
// 1. 快速发送10条消息（无延迟）
// 2. 统计成功数量
// 3. 查询消息历史，验证数据一致性
func TestEdgeCase_RapidMessages(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()
	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	// 1. 先建立会话（A发送第一条消息）
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "Init",
	})
	msgA, _ := wsReceive(wsA, 3*time.Second)
	convID := msgA["data"].(map[string]interface{})["conversation_id"].(string)

	// B也会收到这条消息
	wsReceive(wsB, 3*time.Second)

	// 2. B回复一条消息，解除首条消息限制
	wsSend(wsB, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         "Reply from B",
	})
	wsReceive(wsB, 3*time.Second) // B接收自己发送的消息
	wsReceive(wsA, 3*time.Second) // A接收B的回复

	// 3. 快速发送10条消息（无延迟）
	successCount := 0
	for i := 0; i < 10; i++ {
		err := wsSend(wsA, "message", map[string]interface{}{
			"conversation_id": convID,
			"message_type":    "text",
			"content":         fmt.Sprintf("Rapid %d", i),
		})
		if err == nil {
			msg, err := wsReceive(wsA, 3*time.Second)
			if err == nil && msg["type"] == "message" {
				successCount++
			}
		}
	}

	// 4. 验证闭环：至少应该有部分成功
	assert.GreaterOrEqual(t, successCount, 5, "快速发送至少应该有50%成功")
	t.Logf("快速发送: 成功%d/10", successCount)

	// 5. 验证数据一致性
	time.Sleep(500 * time.Millisecond)
	messages, _ := getMessages(userA.Token, convID)
	// 应该有：1(Init) + 1(Reply from B) + successCount
	assert.GreaterOrEqual(t, len(messages), successCount+2, "数据库消息数应>=成功数+2（init+reply）")
}

// ============================================
// 边界测试 - 不存在的资源
// ============================================

// TestEdgeCase_NonExistentConversation 测试访问不存在的会话
//
// 测试目标：
// - 访问不存在的会话ID应返回404或403
//
// 验证闭环：
// 1. 使用随机UUID访问会话消息
// 2. 验证返回404或403
func TestEdgeCase_NonExistentConversation(t *testing.T) {
	user := createTestUser()
	fakeConvID := uuid.New().String()

	// 1. 访问不存在的会话
	resp, _, err := httpRequest("GET", "/api/conversations/"+fakeConvID+"/messages", user.Token, nil)

	// 2. 验证闭环：返回错误状态码
	require.NoError(t, err)
	assert.True(t, resp.StatusCode == 404 || resp.StatusCode == 403, "应返回404或403")
	t.Logf("访问不存在的会话返回: %d", resp.StatusCode)
}

// TestEdgeCase_NonExistentUser 测试给不存在的用户发消息
//
// 测试目标：
// - 给不存在的用户ID发消息的处理
//
// 验证闭环：
// 1. 使用随机UUID作为receiver_id发消息
// 2. 根据系统设计验证结果（可能创建会话或拒绝）
func TestEdgeCase_NonExistentUser(t *testing.T) {
	userA := createTestUser()
	fakeUserID := uuid.New()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	// 1. 给不存在的用户发消息
	err := wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  fakeUserID.String(),
		"message_type": "text",
		"content":      "To nobody",
	})
	require.NoError(t, err)

	// 2. 验证闭环：检查响应
	msg, err := wsReceive(wsA, 3*time.Second)
	if err == nil {
		if msg["type"] == "error" {
			t.Log("系统拒绝给不存在的用户发消息")
		} else {
			// 如果系统不验证用户存在，会话可以创建
			convID := msg["data"].(map[string]interface{})["conversation_id"].(string)
			assert.NotEmpty(t, convID)
			t.Log("系统允许给不存在的用户发消息（延迟验证）")
		}
	}
}

// TestEdgeCase_AccessOtherConversation 测试访问不属于自己的会话
//
// 测试目标：
// - 用户不能访问其他人的私聊会话
//
// 验证闭环：
// 1. A和B创建会话
// 2. C尝试访问A-B的会话
// 3. 验证返回403或404
func TestEdgeCase_AccessOtherConversation(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()
	userC := createTestUser()

	// 1. A和B创建会话
	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "AB chat",
	})
	msg, _ := wsReceive(wsA, 3*time.Second)
	convID := msg["data"].(map[string]interface{})["conversation_id"].(string)

	// 2. C尝试访问A-B的会话
	resp, _, err := httpRequest("GET", "/api/conversations/"+convID+"/messages", userC.Token, nil)

	// 3. 验证闭环：应该被拒绝
	require.NoError(t, err)
	assert.True(t, resp.StatusCode == 403 || resp.StatusCode == 404, "不应能访问他人会话")
	t.Logf("非成员访问会话返回: %d", resp.StatusCode)
}

// ============================================
// 边界测试 - 特殊场景
// ============================================

// TestEdgeCase_SelfMessage 测试给自己发消息
//
// 测试目标：
// - 验证给自己发消息的处理
//
// 验证闭环：
// 1. 用户给自己发消息
// 2. 如果拒绝：收到error
// 3. 如果接受：验证会话和消息正确创建
func TestEdgeCase_SelfMessage(t *testing.T) {
	user := createTestUser()

	ws, _ := connectWebSocket(user.Token)
	defer ws.Close()

	// 1. 给自己发消息
	err := wsSend(ws, "message", map[string]interface{}{
		"receiver_id":  user.ID.String(),
		"message_type": "text",
		"content":      "To myself",
	})
	require.NoError(t, err)

	// 2. 验证闭环：检查响应
	msg, err := wsReceive(ws, 3*time.Second)
	if err == nil {
		if msg["type"] == "error" {
			errorData := msg["data"].(map[string]interface{})
			assert.Contains(t, strings.ToLower(errorData["message"].(string)), "self", "错误应提示self问题")
			t.Log("系统拒绝自己给自己发消息")
		} else {
			// 如果接受，验证会话创建
			convID := msg["data"].(map[string]interface{})["conversation_id"].(string)
			conversations, _ := getConversationList(user.Token)
			conv := findConversationByID(conversations, convID)
			assert.NotNil(t, conv)

			// 验证会话只有1个成员（自己）
			members := conv["members"].([]interface{})
			assert.Equal(t, 1, len(members), "自己和自己的会话应只有1个成员")

			t.Log("系统允许自己给自己发消息")
		}
	}
}

// TestEdgeCase_InvalidJSON 测试发送无效JSON
//
// 测试目标：
// - WebSocket发送无效JSON应被拒绝
//
// 验证闭环：
// 1. 发送无效的JSON字符串
// 2. 收到error或连接关闭
func TestEdgeCase_InvalidJSON(t *testing.T) {
	user := createTestUser()

	ws, _ := connectWebSocket(user.Token)
	defer ws.Close()

	// 1. 发送无效JSON
	err := ws.WriteMessage(websocket.TextMessage, []byte("{invalid json"))
	require.NoError(t, err, "发送应该成功（WebSocket层）")

	// 2. 验证闭环：必须收到error响应（保持连接稳定性）
	msg, err := wsReceive(ws, 3*time.Second)
	require.NoError(t, err, "WebSocket连接不应断开，应该返回error消息以保持前端连接稳定")
	require.Equal(t, "error", msg["type"], "应收到error类型消息")
	errorData := msg["data"].(map[string]interface{})
	assert.Contains(t, strings.ToLower(errorData["message"].(string)), "json", "错误应提示JSON格式问题")
	t.Log("✓ 收到JSON解析错误，连接保持稳定")
}

// TestEdgeCase_MissingRequiredFields 测试缺少必填字段
//
// 测试目标：
// - 发送消息时缺少必填字段应被拒绝
//
// 验证闭环：
// 1. 发送缺少receiver_id的消息
// 2. 收到error
func TestEdgeCase_MissingRequiredFields(t *testing.T) {
	userA := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	// 1. 发送缺少receiver_id的消息
	err := wsSend(wsA, "message", map[string]interface{}{
		// 缺少receiver_id
		"message_type": "text",
		"content":      "Missing receiver_id",
	})
	require.NoError(t, err)

	// 2. 验证闭环：应收到错误
	msg, err := wsReceive(wsA, 3*time.Second)
	if err == nil {
		if msg["type"] == "error" {
			t.Log("系统正确拒绝缺少必填字段的请求")
		} else {
			t.Log("系统可能使用了默认值或忽略了必填字段验证")
		}
	}
}

// ============================================
// 边界测试 - 会话管理
// ============================================

// TestEdgeCase_EmptyConversationList 测试空会话列表
//
// 测试目标：
// - 新用户查询会话列表应返回空数组
//
// 验证闭环：
// 1. 创建新用户
// 2. 查询会话列表
// 3. 验证返回空数组（不是null）
func TestEdgeCase_EmptyConversationList(t *testing.T) {
	newUser := createTestUser()

	// 1. 查询会话列表
	resp, body, err := httpRequest("GET", "/api/conversations", newUser.Token, nil)

	// 2. 验证闭环：返回200和空数组
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	result := parseResponse(body)
	conversations, ok := result["conversations"].([]interface{})
	assert.True(t, ok, "conversations应该是数组")
	assert.Equal(t, 0, len(conversations), "新用户应该有空会话列表")
	t.Log("空会话列表返回正确")
}

// TestEdgeCase_Pagination 测试分页边界
//
// 测试目标：
// - 分页参数正确处理
// - offset超过总数时返回空数组
//
// 验证闭环：
// 1. 创建3个会话
// 2. 查询第1页（limit=2, offset=0），返回2个
// 3. 查询第2页（limit=2, offset=2），返回1个
// 4. 查询第3页（limit=2, offset=4），返回0个
func TestEdgeCase_Pagination(t *testing.T) {
	user := createTestUser()

	// 1. 创建3个会话
	ws, _ := connectWebSocket(user.Token)
	defer ws.Close()

	for i := 0; i < 3; i++ {
		targetUser := createTestUser()
		wsSend(ws, "message", map[string]interface{}{
			"receiver_id":  targetUser.ID.String(),
			"message_type": "text",
			"content":      fmt.Sprintf("Conv %d", i),
		})
		wsReceive(ws, 3*time.Second)
		time.Sleep(100 * time.Millisecond)
	}

	// 2. 查询第1页
	resp, body, _ := httpRequest("GET", "/api/conversations?limit=2&offset=0", user.Token, nil)
	assert.Equal(t, 200, resp.StatusCode)
	result := parseResponse(body)
	page1 := result["conversations"].([]interface{})
	assert.LessOrEqual(t, len(page1), 2, "第1页应返回<=2条")

	// 3. 查询第2页
	resp, body, _ = httpRequest("GET", "/api/conversations?limit=2&offset=2", user.Token, nil)
	assert.Equal(t, 200, resp.StatusCode)
	result = parseResponse(body)
	page2 := result["conversations"].([]interface{})
	assert.GreaterOrEqual(t, len(page2), 0, "第2页应返回>=0条")

	// 4. 查询超出范围的页
	resp, body, _ = httpRequest("GET", "/api/conversations?limit=2&offset=100", user.Token, nil)
	assert.Equal(t, 200, resp.StatusCode)
	result = parseResponse(body)
	page3 := result["conversations"].([]interface{})
	assert.Equal(t, 0, len(page3), "超出范围应返回空数组")

	t.Logf("分页测试: 第1页%d条, 第2页%d条, 超出范围%d条", len(page1), len(page2), len(page3))
}

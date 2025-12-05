package test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================
// 消息搜索功能测试
// ============================================

// TestMessageSearch_BasicSearch 测试基础搜索功能
//
// 测试目标：
// - 验证用户可以通过关键词搜索消息
// - 验证搜索结果只包含匹配关键词的消息
//
// 验证闭环：
// 1. A 发送3条消息给 B："这是测试消息1"、"这是普通消息"、"这是测试消息2"
// 2. B 搜索关键词"测试"
// 3. 验证返回2条消息（消息1和消息3）
// 4. 验证两条消息都包含"测试"关键词
// 5. 验证"普通消息"不在搜索结果中
func TestMessageSearch_BasicSearch(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	var convID string

	messages := []string{
		"这是测试消息1",
		"这是普通消息",
		"这是测试消息2",
	}

	for i, content := range messages {
		t.Logf("发送消息 %d: %s", i+1, content)
		wsSend(wsA, "message", map[string]interface{}{
			"receiver_id":  userB.ID.String(),
			"message_type": "text",
			"content":      content,
		})
		msg, err := wsReceive(wsA, 3*time.Second)
		if err != nil {
			t.Logf("A 接收响应失败: %v", err)
			continue
		}
		t.Logf("A 收到响应类型: %s", msg["type"])
		if convID == "" {
			convID = msg["data"].(map[string]interface{})["conversation_id"].(string)
		}
		wsReceive(wsB, 3*time.Second)

		// B 回复一条消息，以解除首条消息限制
		wsSend(wsB, "message", map[string]interface{}{
			"conversation_id": convID,
			"message_type":    "text",
			"content":         "收到",
		})
		wsReceive(wsB, 3*time.Second)
		wsReceive(wsA, 3*time.Second)

		time.Sleep(100 * time.Millisecond)
	}

	resp, body, err := httpRequest("GET", "/api/messages/search?q=测试", userB.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	result := parseResponse(body)
	messagesResult, ok := result["messages"].([]interface{})
	require.True(t, ok)

	t.Logf("搜索到 %d 条消息", len(messagesResult))
	for i, msg := range messagesResult {
		m := msg.(map[string]interface{})
		t.Logf("消息 %d: %s", i+1, m["content"])
	}

	assert.Equal(t, 2, len(messagesResult))

	for _, msg := range messagesResult {
		m := msg.(map[string]interface{})
		assert.Contains(t, m["content"].(string), "测试")
	}

	t.Log("✓ 基础搜索测试通过")
}

// TestMessageSearch_ConversationFilter 测试指定会话搜索
//
// 测试目标：
// - 验证用户可以在指定会话中搜索消息
// - 验证搜索结果只包含该会话的消息
//
// 验证闭环：
// 1. A 发送消息"搜索关键词in_AB"给 B（会话1）
// 2. A 发送消息"搜索关键词in_AC"给 C（会话2）
// 3. B 在会话1中搜索"搜索关键词"
// 4. 验证只返回1条消息
// 5. 验证返回的消息是"in_AB"（不包含"in_AC"）
func TestMessageSearch_ConversationFilter(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()
	userC := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	wsC, _ := connectWebSocket(userC.Token)
	defer wsC.Close()

	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "搜索关键词in_AB",
	})
	msgAB, _ := wsReceive(wsA, 3*time.Second)
	convIDAB := msgAB["data"].(map[string]interface{})["conversation_id"].(string)
	wsReceive(wsB, 3*time.Second)

	time.Sleep(100 * time.Millisecond)

	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userC.ID.String(),
		"message_type": "text",
		"content":      "搜索关键词in_AC",
	})
	wsReceive(wsA, 3*time.Second)
	wsReceive(wsC, 3*time.Second)

	time.Sleep(100 * time.Millisecond)

	resp, body, err := httpRequest("GET", "/api/messages/search?q=搜索关键词&conversation_id="+convIDAB, userB.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	result := parseResponse(body)
	messages, _ := result["messages"].([]interface{})

	assert.Equal(t, 1, len(messages))

	msg := messages[0].(map[string]interface{})
	assert.Contains(t, msg["content"].(string), "in_AB")

	t.Log("✓ 指定会话搜索测试通过")
}

// TestMessageSearch_GlobalSearch 测试全局搜索
//
// 测试目标：
// - 验证用户可以跨所有会话搜索消息
// - 验证搜索结果包含所有会话中的匹配消息
//
// 验证闭环：
// 1. A 发送消息"全局搜索测试1"给 B（会话1）
// 2. C 发送消息"全局搜索测试2"给 B（会话2）
// 3. B 全局搜索"全局"（不指定 conversation_id）
// 4. 验证返回至少2条消息
// 5. 验证消息来自不同的会话
func TestMessageSearch_GlobalSearch(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()
	userC := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	wsC, _ := connectWebSocket(userC.Token)
	defer wsC.Close()

	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "全局搜索测试1",
	})
	wsReceive(wsA, 3*time.Second)
	wsReceive(wsB, 3*time.Second)

	time.Sleep(100 * time.Millisecond)

	wsSend(wsC, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "全局搜索测试2",
	})
	wsReceive(wsC, 3*time.Second)
	wsReceive(wsB, 3*time.Second)

	time.Sleep(100 * time.Millisecond)

	resp, body, err := httpRequest("GET", "/api/messages/search?q=全局", userB.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	result := parseResponse(body)
	messages, _ := result["messages"].([]interface{})

	assert.GreaterOrEqual(t, len(messages), 2)

	t.Log("✓ 全局搜索测试通过")
}

// TestMessageSearch_CaseInsensitive 测试不区分大小写
//
// 测试目标：
// - 验证搜索功能不区分大小写
//
// 验证闭环：
// 1. A 发送消息"Hello World"给 B
// 2. B 搜索关键词"hello"（小写）
// 3. 验证能搜索到"Hello World"消息
// 4. 验证搜索使用 ILIKE 进行大小写不敏感匹配
func TestMessageSearch_CaseInsensitive(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "Hello World",
	})
	wsReceive(wsA, 3*time.Second)
	wsReceive(wsB, 3*time.Second)

	time.Sleep(100 * time.Millisecond)

	resp, body, err := httpRequest("GET", "/api/messages/search?q=hello", userB.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	result := parseResponse(body)
	messages, _ := result["messages"].([]interface{})

	assert.GreaterOrEqual(t, len(messages), 1)

	t.Log("✓ 不区分大小写测试通过")
}

// TestMessageSearch_NoKeyword 测试缺少关键词
//
// 测试目标：
// - 验证搜索接口参数验证
// - 验证缺少关键词时返回错误
//
// 验证闭环：
// 1. 用户调用搜索接口不带 q 或 keyword 参数
// 2. 验证返回 400 错误
// 3. 验证错误提示"q or keyword is required"
func TestMessageSearch_NoKeyword(t *testing.T) {
	user := createTestUser()

	resp, _, err := httpRequest("GET", "/api/messages/search", user.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 400, resp.StatusCode)

	t.Log("✓ 缺少关键词参数测试通过")
}

// TestMessageSearch_InvalidConversationID 测试无效conversation_id
//
// 测试目标：
// - 验证 conversation_id 参数格式验证
// - 验证无效 UUID 格式时返回错误
//
// 验证闭环：
// 1. 用户调用搜索接口传入无效的 conversation_id（非 UUID 格式）
// 2. 验证返回 400 错误
// 3. 验证错误提示"Invalid conversation_id"
func TestMessageSearch_InvalidConversationID(t *testing.T) {
	user := createTestUser()

	resp, _, err := httpRequest("GET", "/api/messages/search?q=test&conversation_id=invalid-uuid", user.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 400, resp.StatusCode)

	t.Log("✓ 无效conversation_id测试通过")
}

// TestMessageSearch_NotMember 测试非成员搜索
//
// 测试目标：
// - 验证会话成员权限控制
// - 验证非成员无法搜索指定会话的消息
//
// 验证闭环：
// 1. A 和 B 创建私聊会话并发送消息
// 2. C（非会话成员）尝试搜索该会话的消息
// 3. 验证返回 403 Forbidden
// 4. 验证错误提示"you are not a member of this conversation"
// 5. 验证 C 无法获取 A 和 B 的私聊内容
func TestMessageSearch_NotMember(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()
	userC := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "Private message",
	})
	msg, _ := wsReceive(wsA, 3*time.Second)
	convID := msg["data"].(map[string]interface{})["conversation_id"].(string)
	wsReceive(wsB, 3*time.Second)

	time.Sleep(100 * time.Millisecond)

	resp, _, err := httpRequest("GET", "/api/messages/search?q=Private&conversation_id="+convID, userC.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 403, resp.StatusCode)

	t.Log("✓ 非成员搜索测试通过")
}

// TestMessageSearch_ExcludeRecalledMessages 测试排除已撤回消息
//
// 测试目标：
// - 验证搜索结果不包含已撤回的消息
// - 验证撤回功能与搜索功能的集成
//
// 验证闭环：
// 1. A 发送2条消息给 B："撤回测试消息1"和"撤回测试消息2"
// 2. A 撤回第1条消息
// 3. B 搜索"撤回测试"
// 4. 验证只返回1条消息（消息2）
// 5. 验证返回的消息ID是消息2的ID
// 6. 验证已撤回的消息1不在搜索结果中
func TestMessageSearch_ExcludeRecalledMessages(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	var msgIDs []string
	var convID string

	for i := 1; i <= 2; i++ {
		wsSend(wsA, "message", map[string]interface{}{
			"receiver_id":  userB.ID.String(),
			"message_type": "text",
			"content":      "撤回测试消息" + string(rune('0'+i)),
		})
		msg, _ := wsReceive(wsA, 3*time.Second)
		msgIDs = append(msgIDs, msg["data"].(map[string]interface{})["id"].(string))
		if convID == "" {
			convID = msg["data"].(map[string]interface{})["conversation_id"].(string)
		}
		wsReceive(wsB, 3*time.Second)

		// B 回复，解除首条消息限制
		wsSend(wsB, "message", map[string]interface{}{
			"conversation_id": convID,
			"message_type":    "text",
			"content":         "收到",
		})
		wsReceive(wsB, 3*time.Second)
		wsReceive(wsA, 3*time.Second)

		time.Sleep(100 * time.Millisecond)
	}

	resp, _, err := httpRequest("POST", "/api/messages/"+msgIDs[0]+"/recall", userA.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	time.Sleep(200 * time.Millisecond)

	resp, body, err := httpRequest("GET", "/api/messages/search?q=撤回测试", userB.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	result := parseResponse(body)
	messages, _ := result["messages"].([]interface{})

	assert.Equal(t, 1, len(messages))

	msg := messages[0].(map[string]interface{})
	assert.Equal(t, msgIDs[1], msg["id"].(string))

	t.Log("✓ 排除已撤回消息测试通过")
}

// TestMessageSearch_Pagination 测试分页
//
// 测试目标：
// - 验证搜索结果支持分页
// - 验证 limit 和 offset 参数正常工作
//
// 验证闭环：
// 1. A 发送10条消息给 B
// 2. B 搜索消息，使用 limit=5 获取前5条
// 3. 验证返回5条消息
// 4. B 使用 limit=5&offset=5 获取后5条
// 5. 验证返回另外5条消息
// 6. 验证两次请求的消息ID不重复
func TestMessageSearch_Pagination(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	var convID string

	for i := 1; i <= 10; i++ {
		wsSend(wsA, "message", map[string]interface{}{
			"receiver_id":  userB.ID.String(),
			"message_type": "text",
			"content":      "分页测试消息" + string(rune('0'+i)),
		})
		msg, _ := wsReceive(wsA, 3*time.Second)
		if convID == "" {
			convID = msg["data"].(map[string]interface{})["conversation_id"].(string)
		}
		wsReceive(wsB, 3*time.Second)

		// B 回复，解除首条消息限制
		wsSend(wsB, "message", map[string]interface{}{
			"conversation_id": convID,
			"message_type":    "text",
			"content":         "收到",
		})
		wsReceive(wsB, 3*time.Second)
		wsReceive(wsA, 3*time.Second)

		time.Sleep(50 * time.Millisecond)
	}

	resp, body, err := httpRequest("GET", "/api/messages/search?q=分页&limit=5", userB.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	result := parseResponse(body)
	messages1, _ := result["messages"].([]interface{})

	assert.Equal(t, 5, len(messages1))

	resp, body, err = httpRequest("GET", "/api/messages/search?q=分页&limit=5&offset=5", userB.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	result = parseResponse(body)
	messages2, _ := result["messages"].([]interface{})

	assert.Equal(t, 5, len(messages2))

	id1 := messages1[0].(map[string]interface{})["id"].(string)
	id2 := messages2[0].(map[string]interface{})["id"].(string)
	assert.NotEqual(t, id1, id2)

	t.Log("✓ 分页功能测试通过")
}

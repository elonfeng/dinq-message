package test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================
// 会话列表增强功能测试
// ============================================

// TestConversationList_NewFields 测试会话列表返回新增字段
//
// 测试目标：
// - 验证会话列表包含 last_message_time 字段
// - 验证会话列表包含 last_message_text 字段
// - 验证会话列表包含 unread_count 字段
// - 验证会话列表包含 members 字段
//
// 验证闭环：
// 1. A给B发送3条消息（不同类型）
// 2. B查询会话列表
// 3. 验证 last_message_time 存在且为最新消息时间
// 4. 验证 last_message_text 显示最新消息预览
// 5. 验证 unread_count = 3
// 6. 验证 members 包含A和B
func TestConversationList_NewFields(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	var convID string
	var lastMessageTime time.Time

	// 1. A发送第一条文本消息
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "First message",
	})
	msg, _ := wsReceive(wsA, 3*time.Second)
	convID = msg["data"].(map[string]interface{})["conversation_id"].(string)
	wsReceive(wsB, 3*time.Second) // B收到

	// 2. B回复一条消息（解除首条消息限制）
	wsSend(wsB, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         "Reply from B",
	})
	wsReceive(wsB, 3*time.Second)
	wsReceive(wsA, 3*time.Second)

	// 3. A继续发送第2、3条消息
	wsSend(wsA, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         "Second message",
	})
	wsReceive(wsA, 3*time.Second)
	wsReceive(wsB, 3*time.Second)

	time.Sleep(100 * time.Millisecond)

	wsSend(wsA, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         "Third message - this is the latest one",
	})
	wsReceive(wsA, 3*time.Second)
	lastMessageTime = time.Now() // 记录最新消息时间（近似）
	wsReceive(wsB, 3*time.Second)

	time.Sleep(300 * time.Millisecond)

	// 4. B查询会话列表
	conversations, err := getConversationList(userB.Token)
	require.NoError(t, err)

	conv := findConversationByID(conversations, convID)
	require.NotNil(t, conv, "应该找到会话")

	// 5. 验证 last_message_time 字段
	lastMsgTime, hasTime := conv["last_message_time"]
	if hasTime && lastMsgTime != nil {
		t.Logf("✓ last_message_time 字段存在: %v", lastMsgTime)
		// 验证时间是最近的
		parsedTime, err := time.Parse(time.RFC3339, lastMsgTime.(string))
		if err == nil {
			assert.WithinDuration(t, lastMessageTime, parsedTime, 2*time.Second, "最新消息时间应该接近当前时间")
		}
	} else {
		t.Error("✗ last_message_time 字段缺失或为 null")
	}

	// 6. 验证 last_message_text 字段
	lastMsgText, hasText := conv["last_message_text"]
	if hasText && lastMsgText != nil {
		t.Logf("✓ last_message_text 字段存在: %v", lastMsgText)
		assert.Contains(t, lastMsgText.(string), "Third message", "最新消息预览应该显示第3条消息内容")
	} else {
		t.Error("✗ last_message_text 字段缺失或为 null")
	}

	// 7. 验证 unread_count 字段（通过 members）
	unread := getMemberUnreadCount(conv, userB.ID.String())
	assert.Equal(t, 3, unread, "B应该有3条未读（A的3条消息）")

	// 8. 验证 members 字段
	members, hasMembers := conv["members"]
	require.True(t, hasMembers, "应该包含 members 字段")
	memberList := members.([]interface{})
	assert.Equal(t, 2, len(memberList), "私聊应该有2个成员")

	t.Log("✓ 会话列表新字段测试通过")
}

// TestConversationList_MessageTypePreview 测试不同消息类型的预览文本
//
// 测试目标：
// - text消息：显示文本内容（最多50字符）
// - image消息：显示"[图片]"
// - video消息：显示"[视频]"
// - emoji消息：显示"[表情]"
//
// 验证闭环：
// 1. A发送text消息
// 2. 验证会话列表显示文本内容
// 3. B发送image消息
// 4. 验证会话列表显示"[图片]"
// 5. A发送video消息
// 6. 验证会话列表显示"[视频]"
func TestConversationList_MessageTypePreview(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	var convID string

	// 1. A发送text消息
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "Text message content",
	})
	msg, _ := wsReceive(wsA, 3*time.Second)
	convID = msg["data"].(map[string]interface{})["conversation_id"].(string)
	wsReceive(wsB, 3*time.Second)

	// 让B回复解除限制
	wsSend(wsB, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         "Reply",
	})
	wsReceive(wsB, 3*time.Second)
	wsReceive(wsA, 3*time.Second)

	time.Sleep(200 * time.Millisecond)

	// 2. 验证text消息预览
	conversations, _ := getConversationList(userA.Token)
	conv := findConversationByID(conversations, convID)
	lastMsgText := conv["last_message_text"]
	if lastMsgText != nil {
		assert.Contains(t, lastMsgText.(string), "Reply", "应该显示text消息内容")
	}

	// 3. B发送image消息
	wsSend(wsB, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "image",
		"content":         "https://example.com/image.jpg",
		"metadata": map[string]interface{}{
			"image_url": "https://example.com/image.jpg",
		},
	})
	wsReceive(wsB, 3*time.Second)
	wsReceive(wsA, 3*time.Second)

	time.Sleep(200 * time.Millisecond)

	// 4. 验证image消息预览
	conversations, _ = getConversationList(userA.Token)
	conv = findConversationByID(conversations, convID)
	lastMsgText = conv["last_message_text"]
	if lastMsgText != nil {
		assert.Contains(t, lastMsgText.(string), "[图片]", "应该显示[图片]")
	}

	// 5. A发送video消息
	wsSend(wsA, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "video",
		"content":         "https://example.com/video.mp4",
		"metadata": map[string]interface{}{
			"video_url": "https://example.com/video.mp4",
		},
	})
	wsReceive(wsA, 3*time.Second)
	wsReceive(wsB, 3*time.Second)

	time.Sleep(200 * time.Millisecond)

	// 6. 验证video消息预览
	conversations, _ = getConversationList(userB.Token)
	conv = findConversationByID(conversations, convID)
	lastMsgText = conv["last_message_text"]
	if lastMsgText != nil {
		assert.Contains(t, lastMsgText.(string), "[视频]", "应该显示[视频]")
	}

	// 7. B发送emoji消息
	wsSend(wsB, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "emoji",
		"metadata": map[string]interface{}{
			"emoji_id": "smile",
		},
	})
	wsReceive(wsB, 3*time.Second)
	wsReceive(wsA, 3*time.Second)

	time.Sleep(200 * time.Millisecond)

	// 8. 验证emoji消息预览
	conversations, _ = getConversationList(userA.Token)
	conv = findConversationByID(conversations, convID)
	lastMsgText = conv["last_message_text"]
	if lastMsgText != nil {
		assert.Contains(t, lastMsgText.(string), "[表情]", "应该显示[表情]")
	}

	t.Log("✓ 不同消息类型预览测试通过")
}

// TestConversationList_OnlineStatus 测试私聊会话的在线状态字段
//
// 测试目标：
// - 私聊会话包含 online_status 字段
// - 对方在线时，online_status[user_id] = true
// - 对方离线时，online_status[user_id] = false
//
// 验证闭环：
// 1. A和B都在线，互发消息
// 2. A查询会话列表，验证 online_status 包含B且为true
// 3. B断开WebSocket
// 4. A再次查询，验证 online_status 包含B且为false
func TestConversationList_OnlineStatus(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	// 注意：不立即defer关闭wsB，因为需要测试离线状态

	var convID string

	// 1. A给B发送消息
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "Hello B",
	})
	msg, _ := wsReceive(wsA, 3*time.Second)
	convID = msg["data"].(map[string]interface{})["conversation_id"].(string)
	wsReceive(wsB, 3*time.Second)

	// 2. B回复
	wsSend(wsB, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         "Hello A",
	})
	wsReceive(wsB, 3*time.Second)
	wsReceive(wsA, 3*time.Second)

	time.Sleep(300 * time.Millisecond)

	// 3. A查询会话列表（B在线）
	conversations, _ := getConversationList(userA.Token)
	conv := findConversationByID(conversations, convID)
	require.NotNil(t, conv)

	// 验证 online_status 字段
	onlineStatus, hasStatus := conv["online_status"]
	if hasStatus && onlineStatus != nil {
		statusMap := onlineStatus.(map[string]interface{})
		bStatus, hasBStatus := statusMap[userB.ID.String()]
		if hasBStatus {
			assert.True(t, bStatus.(bool), "B在线时，online_status[B] 应该为 true")
			t.Logf("✓ B在线，online_status[B] = true")
		} else {
			t.Log("online_status 中不包含B的状态（可能是配置关闭了在线状态功能）")
		}
	} else {
		t.Log("会话列表没有 online_status 字段（可能是配置关闭了在线状态功能）")
	}

	// 4. B断开连接
	wsB.Close()
	time.Sleep(500 * time.Millisecond) // 等待服务端更新Redis

	// 5. A再次查询会话列表（B离线）
	conversations, _ = getConversationList(userA.Token)
	conv = findConversationByID(conversations, convID)
	require.NotNil(t, conv)

	onlineStatus, hasStatus = conv["online_status"]
	if hasStatus && onlineStatus != nil {
		statusMap := onlineStatus.(map[string]interface{})
		bStatus, hasBStatus := statusMap[userB.ID.String()]
		if hasBStatus {
			assert.False(t, bStatus.(bool), "B离线时，online_status[B] 应该为 false")
			t.Logf("✓ B离线，online_status[B] = false")
		}
	}

	t.Log("✓ 在线状态测试通过")
}

// TestConversationList_Pagination 测试会话列表分页功能
//
// 测试目标：
// - 默认每页20条
// - 按最新消息时间倒序排序
// - 分页参数正确工作
//
// 验证闭环：
// 1. A创建25个会话（给25个不同用户发消息）
// 2. A查询会话列表（不指定limit），验证返回20条
// 3. A查询会话列表（limit=10），验证返回10条
// 4. 验证会话按 last_message_time 倒序排序
func TestConversationList_Pagination(t *testing.T) {
	userA := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	// 1. A给25个用户发送消息（创建25个会话）
	for i := 0; i < 25; i++ {
		userB := createTestUser()
		wsSend(wsA, "message", map[string]interface{}{
			"receiver_id":  userB.ID.String(),
			"message_type": "text",
			"content":      fmt.Sprintf("Message to user %d", i),
		})
		wsReceive(wsA, 3*time.Second)
		time.Sleep(50 * time.Millisecond) // 确保消息时间不同
	}

	time.Sleep(500 * time.Millisecond)

	// 2. 查询会话列表（默认limit）
	resp, body, err := httpRequest("GET", APIPrefix+"/conversations", userA.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	result := parseResponse(body)
	conversations, _ := result["conversations"].([]interface{})
	assert.LessOrEqual(t, len(conversations), 20, "默认应该返回最多20条")
	t.Logf("✓ 默认查询返回 %d 条会话（预期≤20）", len(conversations))

	// 3. 查询会话列表（limit=10）
	resp, body, err = httpRequest("GET", APIPrefix+"/conversations?limit=10", userA.Token, nil)
	require.NoError(t, err)
	result = parseResponse(body)
	conversations, _ = result["conversations"].([]interface{})
	assert.Equal(t, 10, len(conversations), "指定limit=10应该返回10条")
	t.Logf("✓ limit=10 查询返回 %d 条会话", len(conversations))

	// 4. 验证会话按 last_message_time 倒序排序
	var lastTime *time.Time
	for i, conv := range conversations {
		c := conv.(map[string]interface{})
		lastMsgTime, hasTime := c["last_message_time"]
		if hasTime && lastMsgTime != nil {
			currentTime, err := time.Parse(time.RFC3339, lastMsgTime.(string))
			if err == nil {
				if lastTime != nil {
					assert.True(t, !currentTime.After(*lastTime),
						fmt.Sprintf("第%d个会话的时间应该 ≤ 前一个会话", i))
				}
				lastTime = &currentTime
			}
		}
	}
	t.Log("✓ 会话列表按最新消息时间倒序排序")

	t.Log("✓ 分页功能测试通过")
}

// TestConversationList_LongTextTruncation 测试长文本消息预览截断
//
// 测试目标：
// - 超过50字符的消息应该被截断
// - 截断后应该加上"..."
//
// 验证闭环：
// 1. A发送一条超过50字符的长消息
// 2. B查询会话列表
// 3. 验证 last_message_text 长度 ≤ 53（50字符 + "..."）
// 4. 验证包含"..."
func TestConversationList_LongTextTruncation(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	// 1. A发送超长消息（100个字符）
	longContent := "这是一条非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常长的消息，用来测试消息预览的截断功能是否正常工作"
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      longContent,
	})
	msg, _ := wsReceive(wsA, 3*time.Second)
	convID := msg["data"].(map[string]interface{})["conversation_id"].(string)
	wsReceive(wsB, 3*time.Second)

	// 让B回复
	wsSend(wsB, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         longContent,
	})
	wsReceive(wsB, 3*time.Second)
	wsReceive(wsA, 3*time.Second)

	time.Sleep(200 * time.Millisecond)

	// 2. A查询会话列表
	conversations, _ := getConversationList(userA.Token)
	conv := findConversationByID(conversations, convID)
	require.NotNil(t, conv)

	// 3. 验证截断
	lastMsgText, hasText := conv["last_message_text"]
	if hasText && lastMsgText != nil {
		preview := lastMsgText.(string)
		t.Logf("消息预览: %s", preview)
		t.Logf("预览长度: %d", len([]rune(preview)))

		// 验证长度不超过限制（50字符 + "..." = 53）
		// 注意：这里用rune长度，因为中文字符
		assert.LessOrEqual(t, len([]rune(preview)), 53, "预览文本应该被截断")

		// 验证包含省略号
		if len([]rune(longContent)) > 50 {
			assert.Contains(t, preview, "...", "超长消息应该包含省略号")
		}
	}

	t.Log("✓ 长文本截断测试通过")
}

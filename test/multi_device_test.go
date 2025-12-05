package test

import (
	"fmt"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================
// 多设备连接测试
// ============================================

// TestMultiDevice_MaxConnectionLimit 测试最大连接数限制
//
// 测试目标：
// - 同一用户最多允许 3 个设备同时连接
// - 第 4 个设备连接时应被拒绝
//
// 验证闭环：
// 1. 用户 A 建立 3 个 WebSocket 连接
// 2. 所有 3 个连接都成功
// 3. 尝试建立第 4 个连接
// 4. 第 4 个连接被拒绝（收到 CloseMessage）
// 5. 断开第 1 个连接
// 6. 再次尝试连接，应该成功
func TestMultiDevice_MaxConnectionLimit(t *testing.T) {
	userA := createTestUser()

	// 1. 建立前 3 个连接
	var devices []*websocket.Conn
	for i := 0; i < 3; i++ {
		conn, err := connectWebSocket(userA.Token)
		require.NoError(t, err, fmt.Sprintf("设备 %d 连接应该成功", i+1))
		devices = append(devices, conn)
	}

	// 延迟关闭
	for _, conn := range devices {
		defer conn.Close()
	}

	// 2. 尝试建立第 4 个连接
	conn4, err := connectWebSocket(userA.Token)
	if err == nil {
		// 连接成功了，但应该立即被服务器关闭
		// 尝试读取 CloseMessage
		_, _, readErr := conn4.ReadMessage()
		assert.Error(t, readErr, "第 4 个连接应该被服务器关闭")
		conn4.Close()
	} else {
		// 连接直接失败也是可以接受的
		t.Logf("第 4 个连接直接失败: %v", err)
	}

	// 3. 断开第 1 个连接
	devices[0].Close()
	time.Sleep(100 * time.Millisecond) // 等待服务器处理断开

	// 4. 再次尝试连接，应该成功
	conn5, err := connectWebSocket(userA.Token)
	require.NoError(t, err, "断开一个设备后，新连接应该成功")
	defer conn5.Close()

	// 验证连接可用
	err = wsSend(conn5, "heartbeat", map[string]interface{}{})
	assert.NoError(t, err, "新连接应该可以发送消息")
}

// TestMultiDevice_MessageBroadcast 测试多设备消息广播
//
// 测试目标：
// - 用户收到的消息应该同步到所有在线设备
//
// 验证闭环：
// 1. 用户 A 建立 2 个设备连接（设备1、设备2）
// 2. 用户 B 发送消息给 A
// 3. A 的设备1 和 设备2 都收到消息
// 4. 消息内容一致
func TestMultiDevice_MessageBroadcast(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	// 1. A 建立 2 个设备连接
	deviceA1, err := connectWebSocket(userA.Token)
	require.NoError(t, err)
	defer deviceA1.Close()

	deviceA2, err := connectWebSocket(userA.Token)
	require.NoError(t, err)
	defer deviceA2.Close()

	// 2. B 连接并发送消息给 A
	deviceB, err := connectWebSocket(userB.Token)
	require.NoError(t, err)
	defer deviceB.Close()

	testMessage := "Hello from B! " + time.Now().Format("15:04:05")
	err = wsSend(deviceB, "message", map[string]interface{}{
		"receiver_id":  userA.ID.String(),
		"message_type": "text",
		"content":      testMessage,
	})
	require.NoError(t, err)

	// 3. A 的两个设备都应该收到消息
	msg1, err := wsReceive(deviceA1, 3*time.Second)
	require.NoError(t, err, "设备1 应该收到消息")
	assert.Equal(t, "message", msg1["type"])

	msg2, err := wsReceive(deviceA2, 3*time.Second)
	require.NoError(t, err, "设备2 应该收到消息")
	assert.Equal(t, "message", msg2["type"])

	// 4. 验证消息内容一致
	data1 := msg1["data"].(map[string]interface{})
	data2 := msg2["data"].(map[string]interface{})

	assert.Equal(t, data1["id"], data2["id"], "消息 ID 应该相同")
	assert.Equal(t, testMessage, data1["content"], "设备1 收到的内容应该正确")
	assert.Equal(t, testMessage, data2["content"], "设备2 收到的内容应该正确")
}

// TestMultiDevice_UnreadCountSync 测试多设备未读数同步
//
// 测试目标：
// - 一个设备标记已读，其他设备的未读数应该同步更新
//
// 验证闭环：
// 1. 用户 A 建立 2 个设备连接（设备1、设备2）
// 2. 用户 B 发送消息给 A
// 3. A 的两个设备都收到消息和 unread_count_update（未读数+1）
// 4. 设备1 标记已读
// 5. 设备1 和 设备2 都收到 unread_count_update（未读数清零）
// 6. 验证会话列表中的未读数为 0
func TestMultiDevice_UnreadCountSync(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	// 1. A 建立 2 个设备连接
	deviceA1, err := connectWebSocket(userA.Token)
	require.NoError(t, err)
	defer deviceA1.Close()

	deviceA2, err := connectWebSocket(userA.Token)
	require.NoError(t, err)
	defer deviceA2.Close()

	// 2. B 发送消息给 A
	deviceB, err := connectWebSocket(userB.Token)
	require.NoError(t, err)
	defer deviceB.Close()

	err = wsSend(deviceB, "message", map[string]interface{}{
		"receiver_id":  userA.ID.String(),
		"message_type": "text",
		"content":      "Test unread sync",
	})
	require.NoError(t, err)

	// 3. A 的两个设备收到消息（使用 wsReceiveRaw 避免跳过 unread_count_update）
	var conversationID, messageID string
	var foundMessage1, foundUnread1 bool

	// 设备1：收取消息和未读数更新
	for i := 0; i < 10; i++ {
		msg, err := wsReceiveRaw(deviceA1, 2*time.Second)
		if err != nil {
			break
		}
		msgType, _ := msg["type"].(string)
		if msgType == "message" {
			data1 := msg["data"].(map[string]interface{})
			conversationID = data1["conversation_id"].(string)
			messageID = data1["id"].(string)
			foundMessage1 = true
		} else if msgType == "unread_count_update" {
			foundUnread1 = true
		}
		if foundMessage1 && foundUnread1 {
			break
		}
	}
	require.True(t, foundMessage1, "设备1 应该收到消息")
	require.True(t, foundUnread1, "设备1 应该收到未读数更新")

	// 设备2：收取消息和未读数更新
	var foundMessage2, foundUnread2 bool
	for i := 0; i < 10; i++ {
		msg, err := wsReceiveRaw(deviceA2, 2*time.Second)
		if err != nil {
			break
		}
		msgType, _ := msg["type"].(string)
		if msgType == "message" {
			data2 := msg["data"].(map[string]interface{})
			assert.Equal(t, conversationID, data2["conversation_id"], "设备2 收到的会话 ID 应该一致")
			assert.Equal(t, messageID, data2["id"], "设备2 收到的消息 ID 应该一致")
			foundMessage2 = true
		} else if msgType == "unread_count_update" {
			foundUnread2 = true
		}
		if foundMessage2 && foundUnread2 {
			break
		}
	}
	require.True(t, foundMessage2, "设备2 应该收到消息")
	require.True(t, foundUnread2, "设备2 应该收到未读数更新")

	// 4. 设备1 标记已读
	err = wsSend(deviceA1, "read", map[string]interface{}{
		"conversation_id": conversationID,
		"message_id":      messageID,
	})
	require.NoError(t, err)

	// 5. 两个设备都应该收到未读数清零的更新
	var foundClear1 bool
	for i := 0; i < 10; i++ {
		msg, err := wsReceiveRaw(deviceA1, 2*time.Second)
		if err != nil {
			break
		}
		msgType, _ := msg["type"].(string)
		if msgType == "unread_count_update" {
			clearData1 := msg["data"].(map[string]interface{})
			if clearData1["unread_count"].(float64) == 0 {
				foundClear1 = true
				break
			}
		}
	}
	require.True(t, foundClear1, "设备1 应该收到未读数清零")

	var foundClear2 bool
	for i := 0; i < 10; i++ {
		msg, err := wsReceiveRaw(deviceA2, 2*time.Second)
		if err != nil {
			break
		}
		msgType, _ := msg["type"].(string)
		if msgType == "unread_count_update" {
			clearData2 := msg["data"].(map[string]interface{})
			if clearData2["unread_count"].(float64) == 0 {
				foundClear2 = true
				break
			}
		}
	}
	require.True(t, foundClear2, "设备2 应该收到未读数清零")

	// 6. 验证会话列表中的未读数
	time.Sleep(200 * time.Millisecond)
	conversations, err := getConversationList(userA.Token)
	require.NoError(t, err)
	conv := findConversationByID(conversations, conversationID)
	require.NotNil(t, conv)

	unreadCount := getMemberUnreadCount(conv, userA.ID.String())
	assert.Equal(t, 0, unreadCount, "会话列表中的未读数应该为 0")
}

// TestMultiDevice_MarkReadIdempotency 测试多设备标记已读的幂等性
//
// 测试目标：
// - 多个设备并发标记已读时，使用 MAX 逻辑，不会互相覆盖
//
// 验证闭环：
// 1. 用户 A 建立 2 个设备连接
// 2. 用户 B 发送 5 条消息给 A
// 3. 设备1 标记已读到消息3
// 4. 设备2 标记已读到消息5
// 5. 设备1 再标记已读到消息4（比消息5旧）
// 6. 验证最终 last_read_message_id 是消息5（取最大值）
func TestMultiDevice_MarkReadIdempotency(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	// 1. A 建立 2 个设备连接
	deviceA1, err := connectWebSocket(userA.Token)
	require.NoError(t, err)
	defer deviceA1.Close()

	deviceA2, err := connectWebSocket(userA.Token)
	require.NoError(t, err)
	defer deviceA2.Close()

	// 2. B 发送 5 条消息，并同步接收确认
	deviceB, err := connectWebSocket(userB.Token)
	require.NoError(t, err)
	defer deviceB.Close()

	// 禁用首条消息限制功能（允许连续发送多条消息）
	_, _, err = httpRequest("POST", "/api/admin/settings/enable_first_message_limit", userA.Token, map[string]interface{}{
		"value": "false",
	})
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond) // 等待配置生效

	// 测试结束后恢复功能
	defer func() {
		httpRequest("POST", "/api/admin/settings/enable_first_message_limit", userA.Token, map[string]interface{}{
			"value": "true",
		})
	}()

	var messageIDs []string
	var conversationID string

	// 先发送所有5条消息
	for i := 1; i <= 5; i++ {
		err = wsSend(deviceB, "message", map[string]interface{}{
			"receiver_id":  userA.ID.String(),
			"message_type": "text",
			"content":      fmt.Sprintf("Message %d", i),
		})
		require.NoError(t, err)
		time.Sleep(150 * time.Millisecond) // 确保消息顺序和到达
	}

	// 等待消息都到达
	time.Sleep(500 * time.Millisecond)

	// 收集所有 message 类型的消息（耐心等待，容忍中间的更新消息）
	consecutiveTimeouts := 0
	for len(messageIDs) < 5 && consecutiveTimeouts < 5 {
		msg, err := wsReceiveRaw(deviceA1, 300*time.Millisecond)
		if err != nil {
			consecutiveTimeouts++
			continue
		}
		consecutiveTimeouts = 0 // 收到任何消息都重置

		msgType, _ := msg["type"].(string)
		if msgType == "message" {
			data := msg["data"].(map[string]interface{})
			messageIDs = append(messageIDs, data["id"].(string))
			if conversationID == "" {
				conversationID = data["conversation_id"].(string)
			}
		}
		// 其他类型消息（unread_count_update, conversation_update）忽略，继续循环
	}

	require.Equal(t, 5, len(messageIDs), "应该收集到 5 条消息 ID")

	// 清空设备2的接收缓冲区（设备2也会收到这5条消息）
	messageCount2 := 0
	for i := 0; i < 100; i++ {
		msg, err := wsReceiveRaw(deviceA2, 200*time.Millisecond)
		if err != nil {
			if messageCount2 >= 5 {
				break // 已经收到5条消息，可以退出
			}
			continue
		}
		msgType, _ := msg["type"].(string)
		if msgType == "message" {
			messageCount2++
			if messageCount2 >= 5 {
				break
			}
		}
	}

	// 3. 设备1 标记已读到消息3
	err = wsSend(deviceA1, "read", map[string]interface{}{
		"conversation_id": conversationID,
		"message_id":      messageIDs[2], // 消息3
	})
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)

	// 4. 设备2 标记已读到消息5（最新）
	err = wsSend(deviceA2, "read", map[string]interface{}{
		"conversation_id": conversationID,
		"message_id":      messageIDs[4], // 消息5
	})
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)

	// 5. 设备1 再标记已读到消息4（比消息5旧）
	err = wsSend(deviceA1, "read", map[string]interface{}{
		"conversation_id": conversationID,
		"message_id":      messageIDs[3], // 消息4
	})
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)

	// 6. 验证最终 last_read_message_id 是消息5
	conversations, err := getConversationList(userA.Token)
	require.NoError(t, err)
	conv := findConversationByID(conversations, conversationID)
	require.NotNil(t, conv)

	members := conv["members"].([]interface{})
	var lastReadMessageID string
	for _, m := range members {
		member := m.(map[string]interface{})
		if member["user_id"].(string) == userA.ID.String() {
			if lastRead, ok := member["last_read_message_id"].(string); ok {
				lastReadMessageID = lastRead
			}
			break
		}
	}

	assert.Equal(t, messageIDs[4], lastReadMessageID,
		"last_read_message_id 应该是消息5（最新的），不会被旧消息覆盖")
}

// TestMultiDevice_DifferentConversations 测试多设备在不同会话的未读数行为
//
// 测试目标：
// - 设备1 在查看会话A，设备2 在查看会话B
// - 收到会话A的消息，未读数不增加（设备1在看）
// - 收到会话B的消息，未读数不增加（设备2在看）
// - 收到会话C的消息，未读数增加（都没在看）
//
// 验证闭环：
// 1. 用户 A 建立 2 个设备连接
// 2. 创建 3 个会话（A-B, A-C, A-D）
// 3. 设备1 进入会话 A-B
// 4. 设备2 进入会话 A-C
// 5. B 发送消息给 A → 验证 A-B 会话未读数不增加（设备1在看）
// 6. C 发送消息给 A → 验证 A-C 会话未读数不增加（设备2在看）
// 7. D 发送消息给 A → 验证 A-D 会话未读数增加（都没在看）
func TestMultiDevice_DifferentConversations(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()
	userC := createTestUser()
	userD := createTestUser()

	// 禁用首条消息限制功能（允许连续发送多条消息）
	_, _, err := httpRequest("POST", "/api/admin/settings/enable_first_message_limit", userA.Token, map[string]interface{}{
		"value": "false",
	})
	require.NoError(t, err)
	defer func() {
		httpRequest("POST", "/api/admin/settings/enable_first_message_limit", userA.Token, map[string]interface{}{
			"value": "true",
		})
	}()

	// 1. A 建立 2 个设备连接
	deviceA1, err := connectWebSocket(userA.Token)
	require.NoError(t, err)
	defer deviceA1.Close()

	deviceA2, err := connectWebSocket(userA.Token)
	require.NoError(t, err)
	defer deviceA2.Close()

	// 2. 创建 3 个会话
	deviceB, err := connectWebSocket(userB.Token)
	require.NoError(t, err)
	defer deviceB.Close()

	deviceC, err := connectWebSocket(userC.Token)
	require.NoError(t, err)
	defer deviceC.Close()

	deviceD, err := connectWebSocket(userD.Token)
	require.NoError(t, err)
	defer deviceD.Close()

	// 创建会话 A-B
	err = wsSend(deviceB, "message", map[string]interface{}{
		"receiver_id":  userA.ID.String(),
		"message_type": "text",
		"content":      "Init A-B",
	})
	require.NoError(t, err)
	msgAB, err := wsReceive(deviceA1, 3*time.Second)
	require.NoError(t, err)
	convAB := msgAB["data"].(map[string]interface{})["conversation_id"].(string)

	// 创建会话 A-C
	err = wsSend(deviceC, "message", map[string]interface{}{
		"receiver_id":  userA.ID.String(),
		"message_type": "text",
		"content":      "Init A-C",
	})
	require.NoError(t, err)
	msgAC, err := wsReceive(deviceA1, 3*time.Second)
	require.NoError(t, err)
	convAC := msgAC["data"].(map[string]interface{})["conversation_id"].(string)

	// 清空缓冲区
	for i := 0; i < 10; i++ {
		wsReceiveRaw(deviceA1, 500*time.Millisecond)
		wsReceiveRaw(deviceA2, 500*time.Millisecond)
	}

	// 3. 设备1 进入会话 A-B 并标记已读
	err = wsSend(deviceA1, "set_current_conversation", map[string]interface{}{
		"conversation_id": convAB,
	})
	require.NoError(t, err)

	// 标记 A-B 会话的初始消息为已读（清零未读数）
	initMsgIDAB := msgAB["data"].(map[string]interface{})["id"].(string)
	err = wsSend(deviceA1, "read", map[string]interface{}{
		"conversation_id": convAB,
		"message_id":      initMsgIDAB,
	})
	require.NoError(t, err)

	// 4. 设备2 进入会话 A-C 并标记已读
	err = wsSend(deviceA2, "set_current_conversation", map[string]interface{}{
		"conversation_id": convAC,
	})
	require.NoError(t, err)

	// 标记 A-C 会话的初始消息为已读（清零未读数）
	initMsgIDAC := msgAC["data"].(map[string]interface{})["id"].(string)
	err = wsSend(deviceA2, "read", map[string]interface{}{
		"conversation_id": convAC,
		"message_id":      initMsgIDAC,
	})
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)

	// 5. B 发送消息 → A-B 未读数不应增加
	err = wsSend(deviceB, "message", map[string]interface{}{
		"receiver_id":  userA.ID.String(),
		"message_type": "text",
		"content":      "Test from B",
	})
	require.NoError(t, err)
	time.Sleep(300 * time.Millisecond)

	conversations1, _ := getConversationList(userA.Token)
	conv1 := findConversationByID(conversations1, convAB)
	unreadAB1 := getMemberUnreadCount(conv1, userA.ID.String())
	assert.Equal(t, 0, unreadAB1, "A-B 会话未读数应该为 0（设备1在查看）")

	// 6. C 发送消息 → A-C 未读数不应增加
	err = wsSend(deviceC, "message", map[string]interface{}{
		"receiver_id":  userA.ID.String(),
		"message_type": "text",
		"content":      "Test from C",
	})
	require.NoError(t, err)
	time.Sleep(300 * time.Millisecond)

	conversations2, _ := getConversationList(userA.Token)
	conv2 := findConversationByID(conversations2, convAC)
	unreadAC := getMemberUnreadCount(conv2, userA.ID.String())
	assert.Equal(t, 0, unreadAC, "A-C 会话未读数应该为 0（设备2在查看）")

	// 7. D 发送消息 → A-D 未读数应该增加
	err = wsSend(deviceD, "message", map[string]interface{}{
		"receiver_id":  userA.ID.String(),
		"message_type": "text",
		"content":      "Test from D",
	})
	require.NoError(t, err)
	time.Sleep(500 * time.Millisecond)

	// 通过 HTTP API 查找会话 A-D
	conversations3, _ := getConversationList(userA.Token)
	var convAD string
	for _, conv := range conversations3 {
		convMap := conv.(map[string]interface{})
		// 找到不是 convAB 和 convAC 的会话（即 A-D）
		convID := convMap["id"].(string)
		if convID != convAB && convID != convAC {
			convAD = convID
			break
		}
	}

	require.NotEmpty(t, convAD, "应该创建了会话 A-D")

	conv3 := findConversationByID(conversations3, convAD)
	unreadAD := getMemberUnreadCount(conv3, userA.ID.String())
	assert.Greater(t, unreadAD, 0, "A-D 会话未读数应该增加（两个设备都没在查看）")
}

// TestMultiDevice_ConversationUpdate 测试多设备会话更新推送
//
// 测试目标：
// - 会话更新（最新消息）应该推送到所有设备
//
// 验证闭环：
// 1. 用户 A 建立 2 个设备连接
// 2. 用户 B 发送消息给 A
// 3. A 的两个设备都收到 conversation_update
// 4. 验证 conversation_update 包含最新消息内容和时间
func TestMultiDevice_ConversationUpdate(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	// 禁用首条消息限制功能（允许连续发送多条消息）
	_, _, err := httpRequest("POST", "/api/admin/settings/enable_first_message_limit", userA.Token, map[string]interface{}{
		"value": "false",
	})
	require.NoError(t, err)
	defer func() {
		httpRequest("POST", "/api/admin/settings/enable_first_message_limit", userA.Token, map[string]interface{}{
			"value": "true",
		})
	}()

	// 1. A 建立 2 个设备连接
	deviceA1, err := connectWebSocket(userA.Token)
	require.NoError(t, err)
	defer deviceA1.Close()

	deviceA2, err := connectWebSocket(userA.Token)
	require.NoError(t, err)
	defer deviceA2.Close()

	// 2. B 发送消息给 A
	deviceB, err := connectWebSocket(userB.Token)
	require.NoError(t, err)
	defer deviceB.Close()

	testMessage := "Test conversation update"
	err = wsSend(deviceB, "message", map[string]interface{}{
		"receiver_id":  userA.ID.String(),
		"message_type": "text",
		"content":      testMessage,
	})
	require.NoError(t, err)

	// 3. 两个设备都收到消息和 conversation_update（使用 wsReceiveRaw）
	var foundMsg1, foundConvUpdate1 bool
	for i := 0; i < 10; i++ {
		msg, err := wsReceiveRaw(deviceA1, 2*time.Second)
		if err != nil {
			break
		}
		msgType, _ := msg["type"].(string)
		if msgType == "message" {
			foundMsg1 = true
		} else if msgType == "conversation_update" {
			foundConvUpdate1 = true
		}
		if foundMsg1 && foundConvUpdate1 {
			break
		}
	}
	require.True(t, foundMsg1, "设备1 应该收到消息")
	require.True(t, foundConvUpdate1, "设备1 应该收到 conversation_update")

	var foundMsg2, foundConvUpdate2 bool
	for i := 0; i < 10; i++ {
		msg, err := wsReceiveRaw(deviceA2, 2*time.Second)
		if err != nil {
			break
		}
		msgType, _ := msg["type"].(string)
		if msgType == "message" {
			foundMsg2 = true
		} else if msgType == "conversation_update" {
			foundConvUpdate2 = true
		}
		if foundMsg2 && foundConvUpdate2 {
			break
		}
	}
	require.True(t, foundMsg2, "设备2 应该收到消息")
	require.True(t, foundConvUpdate2, "设备2 应该收到 conversation_update")

	// 4. 再次收取以验证内容（因为已经消耗掉了）
	// 重新发送一条消息来测试
	testMessage2 := "Test conversation update 2"
	err = wsSend(deviceB, "message", map[string]interface{}{
		"receiver_id":  userA.ID.String(),
		"message_type": "text",
		"content":      testMessage2,
	})
	require.NoError(t, err)

	// 5. 收取并验证 conversation_update
	var convUpdate1 map[string]interface{}
	for i := 0; i < 10; i++ {
		msg, err := wsReceiveRaw(deviceA1, 2*time.Second)
		if err != nil {
			break
		}
		msgType, _ := msg["type"].(string)
		if msgType == "conversation_update" {
			convUpdate1 = msg
			break
		}
	}
	require.NotNil(t, convUpdate1, "设备1 应该收到 conversation_update")

	var convUpdate2 map[string]interface{}
	for i := 0; i < 10; i++ {
		msg, err := wsReceiveRaw(deviceA2, 2*time.Second)
		if err != nil {
			break
		}
		msgType, _ := msg["type"].(string)
		if msgType == "conversation_update" {
			convUpdate2 = msg
			break
		}
	}
	require.NotNil(t, convUpdate2, "设备2 应该收到 conversation_update")

	// 6. 验证更新内容
	data1 := convUpdate1["data"].(map[string]interface{})
	assert.Equal(t, testMessage2, data1["last_message_text"], "设备1 收到的最新消息内容应该正确")
	assert.NotEmpty(t, data1["last_message_time"], "设备1 应该收到最新消息时间")

	data2 := convUpdate2["data"].(map[string]interface{})
	assert.Equal(t, testMessage2, data2["last_message_text"], "设备2 收到的最新消息内容应该正确")
	assert.NotEmpty(t, data2["last_message_time"], "设备2 应该收到最新消息时间")

	// 7. 验证两个设备收到的内容一致
	assert.Equal(t, data1["conversation_id"], data2["conversation_id"], "会话 ID 应该一致")
	assert.Equal(t, data1["last_message_text"], data2["last_message_text"], "最新消息内容应该一致")
}

// TestMultiDevice_ReadReceiptBroadcast 测试多设备已读回执广播
//
// 测试目标：
// - 启用已读回执时，一个设备标记已读，其他设备应该收到已读回执
//
// 验证闭环：
// 1. 用户 A 建立 2 个设备连接
// 2. 用户 B 建立 1 个设备连接
// 3. B 发送消息给 A
// 4. A 的设备1 标记已读
// 5. B 的设备收到 read 类型的已读回执（包含 reader_id）
// 6. A 的设备2 也应该收到 read 回执（如果启用了已读回执功能）
func TestMultiDevice_ReadReceiptBroadcast(t *testing.T) {
	// 注意：此测试依赖于 enable_read_receipt 功能是否启用
	// 如果未启用，跳过此测试
	t.Skip("需要启用 enable_read_receipt 功能才能运行此测试")

	userA := createTestUser()
	userB := createTestUser()

	// 1. A 建立 2 个设备连接
	deviceA1, err := connectWebSocket(userA.Token)
	require.NoError(t, err)
	defer deviceA1.Close()

	deviceA2, err := connectWebSocket(userA.Token)
	require.NoError(t, err)
	defer deviceA2.Close()

	// 2. B 建立连接
	deviceB, err := connectWebSocket(userB.Token)
	require.NoError(t, err)
	defer deviceB.Close()

	// 3. B 发送消息给 A
	err = wsSend(deviceB, "message", map[string]interface{}{
		"receiver_id":  userA.ID.String(),
		"message_type": "text",
		"content":      "Test read receipt",
	})
	require.NoError(t, err)

	// A 收到消息
	msgA, err := wsReceive(deviceA1, 3*time.Second)
	require.NoError(t, err)
	dataA := msgA["data"].(map[string]interface{})
	conversationID := dataA["conversation_id"].(string)
	messageID := dataA["id"].(string)

	wsReceive(deviceA2, 3*time.Second) // 设备2 也收到

	// 4. 设备1 标记已读
	err = wsSend(deviceA1, "read", map[string]interface{}{
		"conversation_id": conversationID,
		"message_id":      messageID,
	})
	require.NoError(t, err)

	// 5. B 应该收到已读回执
	readReceipt, err := wsReceiveMessageType(deviceB, "read", 3*time.Second, 10)
	require.NoError(t, err, "B 应该收到已读回执")

	receiptData := readReceipt["data"].(map[string]interface{})
	assert.Equal(t, conversationID, receiptData["conversation_id"], "已读回执的会话 ID 应该正确")
	assert.Equal(t, messageID, receiptData["message_id"], "已读回执的消息 ID 应该正确")
	assert.Equal(t, userA.ID.String(), receiptData["reader_id"], "已读回执应该包含读者 ID")
}

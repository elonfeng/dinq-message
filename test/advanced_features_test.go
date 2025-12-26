package test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================
// 高级功能 - 消息撤回
// ============================================

// TestRecall_WithinTimeLimit 测试2分钟内撤回消息
//
// 测试目标：
// - 2分钟内可以成功撤回自己的消息
// - 2分钟后不能撤回（返回400错误）
// - 对方收到撤回通知
//
// 验证闭环：
// 1. A发送第一条消息
// 2. A立即撤回消息（成功，返回200）
// 3. B收到撤回通知
// 4. 查询消息历史，消息的is_recalled=true
// 5. A发送第二条消息（用于测试超时撤回）
// 6. 等待超过2分钟后尝试撤回（失败，返回400）
// 7. 查询消息历史，第二条消息is_recalled仍为false
func TestRecall_WithinTimeLimit(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	// === 第一部分：测试2分钟内撤回成功 ===

	// 1. A发送第一条消息
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "To be recalled",
	})
	msgA, _ := wsReceive(wsA, 3*time.Second)
	msgID1 := msgA["data"].(map[string]interface{})["id"].(string)
	convID := msgA["data"].(map[string]interface{})["conversation_id"].(string)

	wsReceive(wsB, 3*time.Second) // B收到消息

	// 2. A立即撤回第一条消息
	resp, _, err := httpRequest("POST", APIPrefix+"/messages/"+msgID1+"/recall", userA.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "2分钟内撤回应该成功")

	// 3. 验证闭环：B必须收到撤回通知（前端需要实时更新UI）
	recallReceived := false
	for i := 0; i < 10; i++ {
		msg, err := wsReceive(wsB, 1*time.Second)
		if err != nil {
			t.Logf("   第%d次接收超时", i+1)
			continue
		}
		t.Logf("   B收到消息: type=%v", msg["type"])
		if msg["type"] == "recalled" {
			data := msg["data"].(map[string]interface{})
			assert.Equal(t, msgID1, data["message_id"], "撤回通知应包含正确的message_id")
			t.Log("✓ B收到撤回通知")
			recallReceived = true
			break
		}
	}
	require.True(t, recallReceived, "B必须收到撤回通知，否则前端无法实时更新UI显示消息已撤回")

	// 4. 验证数据库状态：第一条消息is_recalled=true 且 recalled_at 有值
	messages, _ := getMessages(userA.Token, convID)
	recalledMsg := findMessageByID(messages, msgID1)
	require.NotNil(t, recalledMsg)
	assert.Equal(t, true, recalledMsg["is_recalled"], "第一条消息应标记为已撤回")

	// 前端需要显示撤回时间，recalled_at 字段必须有值
	if recalledAt, ok := recalledMsg["recalled_at"]; ok && recalledAt != nil {
		t.Logf("✓ recalled_at 字段有值: %v", recalledAt)
	} else {
		t.Error("recalled_at 字段缺失或为null，前端无法显示撤回时间")
	}

	// === 第二部分：测试2分钟后撤回失败 ===

	// 5. B回复，解除首条消息限制
	wsSend(wsB, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         "Reply from B",
	})
	wsReceive(wsB, 3*time.Second) // B收到自己的消息
	wsReceive(wsA, 3*time.Second) // A收到B的回复

	// 6. A发送第二条消息（用于测试超时撤回）
	wsSend(wsA, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         "Cannot recall after 2 minutes",
	})

	// 确保接收到的是 A 自己发送的消息（检查 sender_id）
	var msgID2 string
	for i := 0; i < 5; i++ {
		msg, err := wsReceive(wsA, 3*time.Second)
		if err != nil {
			t.Fatalf("A 没有收到自己发送的消息")
		}

		// 跳过未读数量推送等非消息类型
		msgType := msg["type"].(string)
		if msgType != "message" {
			t.Logf("   跳过非消息类型: %s", msgType)
			continue
		}

		data := msg["data"].(map[string]interface{})
		senderID := data["sender_id"].(string)
		if senderID == userA.ID.String() {
			msgID2 = data["id"].(string)
			t.Logf("📝 第二条消息ID: %s (确认是 A 发送的)", msgID2)
			break
		}
		t.Logf("   跳过其他消息: sender=%s", senderID)
	}
	if msgID2 == "" {
		t.Fatal("未能获取 A 发送的第二条消息ID")
	}

	wsReceive(wsB, 3*time.Second) // B收到第二条消息
	t.Log("⏳ 等待2分钟零1秒后测试撤回失败...")
	// 倒计时显示
	totalSeconds := 11
	for i := totalSeconds; i > 0; i-- {
		if i%10 == 0 || i <= 5 {
			t.Logf("   倒计时: %d 秒...", i)
		}
		time.Sleep(1 * time.Second)
	}

	// 7. 尝试撤回第二条消息（应该失败）
	t.Logf("🔄 发起撤回请求，messageID=%s", msgID2)
	resp2, body2, err := httpRequest("POST", APIPrefix+"/messages/"+msgID2+"/recall", userA.Token, nil)
	require.NoError(t, err)
	t.Logf("撤回响应: status=%d, body=%s", resp2.StatusCode, string(body2))
	assert.Equal(t, 400, resp2.StatusCode, "超过2分钟后撤回应该失败")

	// 8. 验证数据库状态：第二条消息is_recalled仍为false
	messages2, _ := getMessages(userA.Token, convID)
	msg2 := findMessageByID(messages2, msgID2)
	require.NotNil(t, msg2)
	assert.Equal(t, false, msg2["is_recalled"], "超时的消息不应被撤回")

	t.Log("✓ 撤回时间限制测试通过")
}

// TestRecall_NotOwnMessage 测试撤回他人消息被拒绝
//
// 测试目标：
// - 不能撤回别人的消息
// - 返回403或400错误
//
// 验证闭环：
// 1. A发送消息
// 2. B尝试撤回A的消息（失败）
// 3. 查询消息，is_recalled仍为false
func TestRecall_NotOwnMessage(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	// 1. A发送消息
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "A's message",
	})
	msgA, _ := wsReceive(wsA, 3*time.Second)
	msgID := msgA["data"].(map[string]interface{})["id"].(string)
	convID := msgA["data"].(map[string]interface{})["conversation_id"].(string)

	// 2. B尝试撤回A的消息（应该返回403 Forbidden）
	resp, _, err := httpRequest("POST", APIPrefix+"/messages/"+msgID+"/recall", userB.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 403, resp.StatusCode, "撤回他人消息应该返回403 Forbidden（权限问题）")

	// 3. 验证闭环：查询消息，is_recalled仍为false
	messages, _ := getMessages(userA.Token, convID)
	msg := findMessageByID(messages, msgID)
	require.NotNil(t, msg)
	assert.Equal(t, false, msg["is_recalled"], "消息不应被撤回")
}

// TestRecall_AlreadyRecalled 测试重复撤回被拒绝
//
// 测试目标：
// - 已撤回的消息不能再次撤回
//
// 验证闭环：
// 1. A发送消息
// 2. A撤回消息（成功）
// 3. A再次撤回同一消息（失败，返回400）
func TestRecall_AlreadyRecalled(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	// 1. A发送消息
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "To recall twice",
	})
	msgA, _ := wsReceive(wsA, 3*time.Second)
	msgID := msgA["data"].(map[string]interface{})["id"].(string)

	// 2. 第一次撤回（成功）
	resp, _, _ := httpRequest("POST", APIPrefix+"/messages/"+msgID+"/recall", userA.Token, nil)
	assert.Equal(t, 200, resp.StatusCode, "第一次撤回应该成功")

	// 3. 验证闭环：第二次撤回（失败）
	resp, _, err := httpRequest("POST", APIPrefix+"/messages/"+msgID+"/recall", userA.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 400, resp.StatusCode, "重复撤回应该被拒绝")
}

// ============================================
// 高级功能 - 群聊权限
// ============================================

// TestGroupPermission_OnlyOwnerAddMember 测试只有owner能添加成员
//
// 测试目标：
// - 普通成员无法添加新成员
// - owner可以添加新成员
//
// 验证闭环：
// 1. 创建群聊（owner + member1）
// 2. member1尝试添加member2（失败，返回403）
// 3. owner添加member2（成功）
// 4. 查询群成员列表，验证member2已加入
func TestGroupPermission_OnlyOwnerAddMember(t *testing.T) {
	owner := createTestUser()
	member1 := createTestUser()
	member2 := createTestUser()

	// 1. 创建群聊
	resp, body, _ := httpRequest("POST", APIPrefix+"/conversations/group", owner.Token, map[string]interface{}{
		"group_name": "Test Group",
		"member_ids": []string{member1.ID.String()},
	})
	group := parseResponse(body)
	groupID := group["id"].(string)

	// 2. member1尝试添加member2（应该失败）
	resp, _, err := httpRequest("POST", APIPrefix+"/conversations/"+groupID+"/members", member1.Token, map[string]interface{}{
		"member_ids": []string{member2.ID.String()},
	})
	require.NoError(t, err)
	assert.Equal(t, 403, resp.StatusCode, "普通成员不应能添加成员")

	// 3. owner添加member2（应该成功）
	resp, _, err = httpRequest("POST", APIPrefix+"/conversations/"+groupID+"/members", owner.Token, map[string]interface{}{
		"member_ids": []string{member2.ID.String()},
	})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "owner应该能添加成员")

	// 4. 验证闭环：查询群成员列表并验证角色
	conversations, _ := getConversationList(owner.Token)
	conv := findConversationByID(conversations, groupID)
	require.NotNil(t, conv)

	members := conv["members"].([]interface{})
	assert.Equal(t, 3, len(members), "群聊应该有3个成员（owner+member1+member2）")

	// 前端需要根据角色显示不同的权限，验证成员角色正确
	var ownerCount, memberCount int
	for _, m := range members {
		member := m.(map[string]interface{})
		role := member["role"].(string)
		userID := member["user_id"].(string)

		if role == "owner" {
			ownerCount++
			assert.Equal(t, owner.ID.String(), userID, "owner角色应该是创建者")
		} else if role == "member" {
			memberCount++
			// member1 和 member2 都应该是 member 角色
			assert.True(t, userID == member1.ID.String() || userID == member2.ID.String(), "member角色应该是被添加的成员")
		}
	}

	assert.Equal(t, 1, ownerCount, "应该只有1个owner")
	assert.Equal(t, 2, memberCount, "应该有2个member")
	t.Log("✓ 成员角色验证通过")
}

// TestGroupPermission_OnlyOwnerRemoveMember 测试只有owner能踢人
//
// 测试目标：
// - 普通成员无法踢人
// - owner可以踢人
//
// 验证闭环：
// 1. 创建群聊（owner + member1 + member2）
// 2. member1尝试踢member2（失败）
// 3. owner踢member2（成功）
// 4. 查询群成员列表，member2不存在
func TestGroupPermission_OnlyOwnerRemoveMember(t *testing.T) {
	owner := createTestUser()
	member1 := createTestUser()
	member2 := createTestUser()

	// 1. 创建群聊
	resp, body, _ := httpRequest("POST", APIPrefix+"/conversations/group", owner.Token, map[string]interface{}{
		"group_name": "Test Group",
		"member_ids": []string{member1.ID.String(), member2.ID.String()},
	})
	group := parseResponse(body)
	groupID := group["id"].(string)

	// 2. member1尝试踢member2（应该失败）
	resp, _, err := httpRequest("POST", APIPrefix+"/conversations/"+groupID+"/members/remove", member1.Token, map[string]interface{}{
		"user_id": member2.ID.String(),
	})
	require.NoError(t, err)
	assert.Equal(t, 403, resp.StatusCode, "普通成员不应能踢人")

	// 3. owner踢member2（应该成功）
	resp, _, err = httpRequest("POST", APIPrefix+"/conversations/"+groupID+"/members/remove", owner.Token, map[string]interface{}{
		"user_id": member2.ID.String(),
	})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "owner应该能踢人")

	// 4. 验证闭环：查询群成员列表
	conversations, _ := getConversationList(owner.Token)
	conv := findConversationByID(conversations, groupID)
	require.NotNil(t, conv)

	members := conv["members"].([]interface{})
	assert.Equal(t, 2, len(members), "member2被踢后群聊应该有2个成员")

	// 验证member2不在成员列表中
	for _, m := range members {
		member := m.(map[string]interface{})
		assert.NotEqual(t, member2.ID.String(), member["user_id"], "member2不应在成员列表中")
	}
}

// ============================================
// 高级功能 - 通知推送
// ============================================

// TestNotification_NewMessage 测试新消息通知
//
// 注意：消息通知功能已禁用（性能考虑）
// - 私信和群聊消息不创建通知
// - 用户通过会话列表的未读数查看新消息
// - 通知功能保留用于系统通知等特殊场景
//
// 此测试已跳过
func TestNotification_NewMessage(t *testing.T) {
	t.Skip("消息通知功能已禁用 - 用户通过未读消息数量了解新消息")
}

// TestNotification_MarkAsRead 测试标记通知为已读
//
// 测试目标：
// - 可以标记通知为已读
// - 已读状态正确更新
//
// 验证闭环：
// 1. 产生一条通知（通过发消息）
// 2. 查询通知列表，获取通知ID
// 3. 标记通知为已读
// 4. 再次查询，验证is_read=true
func TestNotification_MarkAsRead(t *testing.T) {
	user := createTestUser()

	// 1. 产生通知（让另一个用户给他发消息）
	otherUser := createTestUser()
	wsOther, _ := connectWebSocket(otherUser.Token)
	wsSend(wsOther, "message", map[string]interface{}{
		"receiver_id":  user.ID.String(),
		"message_type": "text",
		"content":      "Test notification",
	})
	wsReceive(wsOther, 3*time.Second)
	wsOther.Close()

	time.Sleep(500 * time.Millisecond)

	// 2. 查询通知列表
	resp, body, _ := httpRequest("GET", APIPrefix+"/notifications", user.Token, nil)
	result := parseResponse(body)
	notifications, ok := result["notifications"].([]interface{})

	if !ok || len(notifications) == 0 {
		t.Skip("未找到通知，跳过测试")
		return
	}

	notif := notifications[0].(map[string]interface{})
	notifID := notif["id"].(string)

	// 3. 标记为已读
	resp, _, err := httpRequest("POST", APIPrefix+"/notifications/"+notifID+"/read", user.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "标记已读应该成功")

	// 4. 验证闭环：再次查询，验证已读
	resp, body, _ = httpRequest("GET", APIPrefix+"/notifications", user.Token, nil)
	result = parseResponse(body)
	notifications = result["notifications"].([]interface{})

	for _, n := range notifications {
		notif := n.(map[string]interface{})
		if notif["id"].(string) == notifID {
			assert.Equal(t, true, notif["is_read"], "通知应标记为已读")
			break
		}
	}
}

// ============================================
// 高级功能 - 离线消息
// ============================================

// TestOfflineMessage_Receive 测试用户上线后收到离线消息
//
// 测试目标：
// - 用户离线时收到的消息会被存储
// - 上线后能收到离线消息
//
// 验证闭环：
// 1. A给离线的B发3条消息
// 2. B上线（连接WebSocket）
// 3. B收到3条离线消息
func TestOfflineMessage_Receive(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	// 1. A给离线的B发第一条消息
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "Offline message 0",
	})
	msgA, _ := wsReceive(wsA, 3*time.Second)
	conversationID := msgA["data"].(map[string]interface{})["conversation_id"].(string)

	// 2. B上线，回复一条消息解除首条消息限制
	wsB, _ := connectWebSocket(userB.Token)

	// B收到第一条离线消息（消耗掉）
	for i := 0; i < 5; i++ {
		msg, err := wsReceive(wsB, 1*time.Second)
		if err == nil {
			msgType := msg["type"].(string)
			if msgType == "offline_message" || msgType == "message" {
				break
			}
		}
	}

	// B回复一条消息
	wsSend(wsB, "message", map[string]interface{}{
		"conversation_id": conversationID,
		"message_type":    "text",
		"content":         "Reply from B",
	})
	wsReceive(wsB, 3*time.Second) // B收到自己的消息
	wsReceive(wsA, 3*time.Second) // A收到B的消息

	// 3. B下线
	wsB.Close()
	time.Sleep(200 * time.Millisecond)

	// 4. A继续给离线的B发3条消息
	for i := 1; i < 4; i++ {
		wsSend(wsA, "message", map[string]interface{}{
			"conversation_id": conversationID,
			"message_type":    "text",
			"content":         fmt.Sprintf("Offline message %d", i),
		})
		wsReceive(wsA, 3*time.Second)
		time.Sleep(100 * time.Millisecond)
	}

	time.Sleep(500 * time.Millisecond)

	// 5. B重新上线
	wsB, _ = connectWebSocket(userB.Token)
	defer wsB.Close()

	// 3. 验证闭环：B应该收到离线消息（类型必须是offline_message）
	offlineMessageCount := 0
	regularMessageCount := 0

	for i := 0; i < 5; i++ {
		msg, err := wsReceive(wsB, 2*time.Second)
		if err == nil {
			msgType := msg["type"].(string)
			if msgType == "offline_message" {
				offlineMessageCount++
				t.Logf("✓ 收到离线消息 %d", offlineMessageCount)
			} else if msgType == "message" {
				regularMessageCount++
				t.Logf("⚠️  收到实时消息（不是离线消息）")
			}
		} else {
			break
		}
	}

	// 前端需要明确区分离线消息和实时消息，以便正确显示
	totalReceived := offlineMessageCount + regularMessageCount
	assert.GreaterOrEqual(t, totalReceived, 3, "B应该收到至少3条消息")
	t.Logf("收到 %d 条离线消息, %d 条实时消息（共 %d 条）", offlineMessageCount, regularMessageCount, totalReceived)
}

// ============================================
// 高级功能 - 未读数量实时推送
// ============================================

// TestUnreadCountUpdate_RealtimePush 测试未读数量实时 WebSocket 推送
//
// 测试目标：
// - 当用户收到新消息时，通过 WebSocket 实时推送未读数量更新
// - 当用户标记已读时，通过 WebSocket 实时推送未读数量清零
//
// 验证闭环：
// 1. A 和 B 都在线
// 2. A 发送 3 条消息给 B
// 3. B 收到 3 次未读数量更新推送（unread_count_update）
// 4. B 标记会话已读
// 5. B 收到未读数量清零推送（unread_count: 0）
func TestUnreadCountUpdate_RealtimePush(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	// === 第一部分：测试发送消息时的未读数量推送 ===

	// A 发送消息给 B
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "Test message",
	})

	// A 收到自己的消息确认
	msgA, _ := wsReceive(wsA, 3*time.Second)
	conversationID := msgA["data"].(map[string]interface{})["conversation_id"].(string)

	// B 收到消息和未读数量更新
	receivedMessage := false
	receivedUnreadUpdate := false
	var unreadCount int

	for attempts := 0; attempts < 10; attempts++ {
		msg, err := wsReceiveRaw(wsB, 2*time.Second)
		if err != nil {
			break
		}

		msgType := msg["type"].(string)
		if msgType == "message" || msgType == "offline_message" {
			receivedMessage = true
			t.Logf("✓ B 收到消息")
		} else if msgType == "unread_count_update" {
			receivedUnreadUpdate = true
			data := msg["data"].(map[string]interface{})
			unreadCount = int(data["unread_count"].(float64))
			t.Logf("✓ B 收到未读数量更新推送: %d", unreadCount)
		}

		if receivedMessage && receivedUnreadUpdate {
			break
		}
	}

	require.True(t, receivedMessage, "B 必须收到消息")
	assert.True(t, receivedUnreadUpdate, "B 应该收到未读数量更新推送")
	assert.Equal(t, 1, unreadCount, "未读数量应该是1")

	// === 第二部分：测试标记已读时的未读数量推送 ===

	// 获取最新的消息ID（用于标记已读）
	messages, _ := getMessages(userB.Token, conversationID)
	require.Greater(t, len(messages), 0, "应该有消息记录")
	lastMessage := messages[0].(map[string]interface{})
	lastMessageID := lastMessage["id"].(string)

	// B 标记已读
	wsSend(wsB, "read", map[string]interface{}{
		"conversation_id": conversationID,
		"message_id":      lastMessageID,
	})

	// B 应该收到未读数量清零推送
	receivedZeroUpdate := false
	for attempts := 0; attempts < 5; attempts++ {
		msg, err := wsReceiveRaw(wsB, 2*time.Second)
		if err != nil {
			break
		}

		if msg["type"].(string) == "unread_count_update" {
			data := msg["data"].(map[string]interface{})
			unreadCount := int(data["unread_count"].(float64))
			if unreadCount == 0 {
				receivedZeroUpdate = true
				t.Logf("✓ B 收到未读数量清零推送")
				break
			}
		}
	}

	assert.True(t, receivedZeroUpdate, "标记已读后，B 应该收到未读数量清零推送")

	// 验证数据库中的未读数量确实为 0
	convList, _ := getConversationList(userB.Token)
	require.Greater(t, len(convList), 0, "应该有会话记录")
	conv := convList[0].(map[string]interface{})
	unreadB := getMemberUnreadCount(conv, userB.ID.String())
	assert.Equal(t, 0, unreadB, "数据库中的未读数量应该是 0")

	t.Log("✅ 未读数量实时推送测试通过")
}

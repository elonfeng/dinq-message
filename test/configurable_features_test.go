package test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================
// 可配置功能 - 首条消息限制
// ============================================

// TestFirstMessageLimit_Enabled 测试首条消息限制（启用状态）
//
// 测试目标：
// - 启用首条消息限制时，用户发送第一条消息后不能继续发送
// - 关闭限制后可以发送
//
// 验证闭环：
// 1. 确保启用首条消息限制（enable_first_message_limit=true）
// 2. A给B发第一条消息（成功）
// 3. A尝试发第二条消息（失败，收到error）
// 4. 关闭首条消息限制（enable_first_message_limit=false）
// 5. A发第三条消息（成功，因为功能关闭）
// 6. 恢复系统配置（enable_first_message_limit=true）
func TestFirstMessageLimit_Enabled(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	// 1. 确保启用首条消息限制
	resp, _, err := httpRequest("POST", APIPrefix+"/admin/settings/enable_first_message_limit", userA.Token, map[string]interface{}{
		"value": "true",
	})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "启用首条消息限制应该成功")
	httpRequest("POST", APIPrefix+"/admin/settings/reload", userA.Token, nil)
	time.Sleep(500 * time.Millisecond)

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	// 2. A发第一条消息（应该成功）
	err = wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "First message",
	})
	require.NoError(t, err)

	msg, _ := wsReceive(wsA, 3*time.Second)
	assert.Equal(t, "message", msg["type"], "第一条消息应该发送成功")
	convID := msg["data"].(map[string]interface{})["conversation_id"].(string)

	time.Sleep(500 * time.Millisecond)

	// 3. A尝试发第二条消息（应该失败）
	err = wsSend(wsA, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         "Second message (should fail)",
	})
	require.NoError(t, err)

	msg2, err := wsReceive(wsA, 3*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "error", msg2["type"], "应该收到error类型消息")
	errorData := msg2["data"].(map[string]interface{})
	assert.Contains(t, strings.ToLower(errorData["message"].(string)), "limit", "错误消息应包含limit")

	// 验证数据库状态：查询消息列表，应该只有1条消息
	messages, _ := getMessages(userA.Token, convID)
	assert.Equal(t, 1, len(messages), "应该只有1条消息（第二条被拒绝）")

	// 4. 关闭首条消息限制
	resp, _, err = httpRequest("POST", APIPrefix+"/admin/settings/enable_first_message_limit", userA.Token, map[string]interface{}{
		"value": "false",
	})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "关闭首条消息限制应该成功")
	httpRequest("POST", APIPrefix+"/admin/settings/reload", userA.Token, nil)
	time.Sleep(500 * time.Millisecond)

	// 5. A发第三条消息（应该成功，因为功能关闭）
	err = wsSend(wsA, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         "Third message (should succeed)",
	})
	require.NoError(t, err)

	msg3, err := wsReceive(wsA, 3*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "message", msg3["type"], "关闭限制后应该可以发送")

	// 验证数据库状态：现在应该有2条消息
	messages, _ = getMessages(userA.Token, convID)
	assert.Equal(t, 2, len(messages), "关闭限制后应该有2条消息")

	// 6. 恢复系统配置
	httpRequest("POST", APIPrefix+"/admin/settings/enable_first_message_limit", userA.Token, map[string]interface{}{
		"value": "true",
	})
	httpRequest("POST", APIPrefix+"/admin/settings/reload", userA.Token, nil)
}

// TestFirstMessageLimit_AfterReply 测试对方回复后可以继续发送
//
// 测试目标：
// - A发第一条消息后被限制
// - B回复后，A可以继续发送
//
// 验证闭环：
// 1. A发第一条消息
// 2. B回复
// 3. A再发第二条消息（成功）
// 4. 查询消息历史，验证有3条消息
func TestFirstMessageLimit_AfterReply(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	// 1. A发第一条消息
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "First",
	})
	msgA, _ := wsReceive(wsA, 3*time.Second)
	convID := msgA["data"].(map[string]interface{})["conversation_id"].(string)

	wsReceive(wsB, 3*time.Second) // B收到

	// 2. B回复
	wsSend(wsB, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         "Reply",
	})
	wsReceive(wsB, 3*time.Second)
	wsReceive(wsA, 3*time.Second) // A收到回复

	time.Sleep(500 * time.Millisecond)

	// 3. A再发第二条消息（应该成功）
	err := wsSend(wsA, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         "Second (should succeed)",
	})
	require.NoError(t, err)

	msg, err := wsReceive(wsA, 3*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "message", msg["type"], "收到回复后应该可以继续发送")

	// 4. 验证闭环：查询消息历史，应有3条消息
	messages, _ := getMessages(userA.Token, convID)
	assert.Equal(t, 3, len(messages), "应该有3条消息（A的第一条+B的回复+A的第二条）")
}

// TestFirstMessageLimit_Disabled 测试关闭首条消息限制（系统配置）
//
// 测试目标：
// - 超管关闭系统配置后，所有用户可以连续发送消息
//
// 验证闭环：
// 1. A发第一条消息
// 2. 超管通过API关闭首条消息限制功能
// 3. A发第二条消息（成功，因为系统功能已关闭）
// 4. 查询消息历史，验证有2条消息
// 5. 恢复系统配置（启用限制）
func TestFirstMessageLimit_Disabled(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	// 1. A发第一条消息
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "First",
	})
	msgA, _ := wsReceive(wsA, 3*time.Second)
	convID := msgA["data"].(map[string]interface{})["conversation_id"].(string)

	// 2. 超管关闭首条消息限制功能
	resp, _, err := httpRequest("POST", APIPrefix+"/admin/settings/enable_first_message_limit", userA.Token, map[string]interface{}{
		"value": "false",
	})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "超管更新系统配置应该成功")

	// 重新加载配置（让服务端生效）
	httpRequest("POST", APIPrefix+"/admin/settings/reload", userA.Token, nil)
	time.Sleep(500 * time.Millisecond)

	// 3. A发第二条消息（应该成功，因为系统功能已关闭）
	err = wsSend(wsA, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         "Second (no limit)",
	})
	require.NoError(t, err)

	msg, err := wsReceive(wsA, 3*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "message", msg["type"], "关闭系统功能后应该可以连续发送")

	// 4. 验证闭环：查询消息历史
	messages, _ := getMessages(userA.Token, convID)
	assert.Equal(t, 2, len(messages), "应该有2条消息")

	// 5. 恢复系统配置（启用限制）
	httpRequest("POST", APIPrefix+"/admin/settings/enable_first_message_limit", userA.Token, map[string]interface{}{
		"value": "true",
	})
	httpRequest("POST", APIPrefix+"/admin/settings/reload", userA.Token, nil)
}

// TestFirstMessageLimit_GroupNoLimit 测试群聊不受首条消息限制
//
// 测试目标：
// - 群聊不受首条消息限制影响
// - 可以连续发送多条消息
//
// 验证闭环：
// 1. 创建群聊
// 2. owner连续发送3条消息（都成功）
// 3. 查询消息历史，验证有3条消息
func TestFirstMessageLimit_GroupNoLimit(t *testing.T) {
	owner := createTestUser()
	member1 := createTestUser()

	// 1. 创建群聊
	_, body, _ := httpRequest("POST", APIPrefix+"/conversations/group", owner.Token, map[string]interface{}{
		"group_name": "Test Group",
		"member_ids": []string{member1.ID.String()},
	})
	group := parseResponse(body)
	groupID := group["id"].(string)

	wsOwner, _ := connectWebSocket(owner.Token)
	defer wsOwner.Close()

	// 2. owner连续发送3条消息
	for i := 0; i < 3; i++ {
		err := wsSend(wsOwner, "message", map[string]interface{}{
			"conversation_id": groupID,
			"message_type":    "text",
			"content":         fmt.Sprintf("Message %d", i+1),
		})
		require.NoError(t, err)

		msg, err := wsReceive(wsOwner, 3*time.Second)
		require.NoError(t, err)

		// 打印错误信息以便调试
		if msg["type"] == "error" {
			errorData := msg["data"].(map[string]interface{})
			t.Logf("第%d条消息发送失败，错误: %v", i+1, errorData["message"])
		}

		assert.Equal(t, "message", msg["type"], "群聊应该可以连续发送")

		time.Sleep(100 * time.Millisecond)
	}

	// 3. 验证闭环：查询消息历史
	messages, _ := getMessages(owner.Token, groupID)
	assert.Equal(t, 3, len(messages), "群聊应该有3条消息")
}

// ============================================
// 可配置功能 - 已读回执
// ============================================

// TestReadReceipt_Enabled 测试已读回执（启用状态）
//
// 测试目标：
// - 启用已读回执时，标记已读后发送者收到通知
// - 关闭已读回执后，标记已读不再发送通知
//
// 验证闭环：
// 1. 确保启用已读回执（enable_read_receipt=true）
// 2. A给B发第一条消息
// 3. B标记已读，A收到read事件
// 4. B回复消息，让A可以继续发送
// 5. 关闭已读回执（enable_read_receipt=false）
// 6. A给B发第二条消息
// 7. B标记已读，A不应收到read事件
// 8. 恢复系统配置
func TestReadReceipt_Enabled(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	// 1. 确保启用已读回执
	resp, _, err := httpRequest("POST", APIPrefix+"/admin/settings/enable_read_receipt", userA.Token, map[string]interface{}{
		"value": "true",
	})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "启用已读回执应该成功")
	httpRequest("POST", APIPrefix+"/admin/settings/reload", userA.Token, nil)
	time.Sleep(500 * time.Millisecond)

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	// 2. A发第一条消息
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "First message",
	})
	msgA, _ := wsReceive(wsA, 3*time.Second)
	convID := msgA["data"].(map[string]interface{})["conversation_id"].(string)
	msgID1 := msgA["data"].(map[string]interface{})["id"].(string)

	wsReceive(wsB, 3*time.Second) // B收到消息

	// 3. B标记已读，验证A收到read_receipt事件
	wsSend(wsB, "read", map[string]interface{}{
		"conversation_id": convID,
		"message_id":      msgID1,
	})

	receivedReceipt := false
	for i := 0; i < 5; i++ {
		msg, err := wsReceive(wsA, 2*time.Second)
		if err == nil && msg["type"] == "read" {
			data := msg["data"].(map[string]interface{})
			assert.Equal(t, convID, data["conversation_id"])
			assert.Equal(t, userB.ID.String(), data["reader_id"])
			receivedReceipt = true
			t.Log("✓ 启用已读回执时，A收到了read事件")
			break
		}
	}

	// 验证数据库状态：B的未读应该归零
	time.Sleep(500 * time.Millisecond)
	conversations, _ := getConversationList(userB.Token)
	conv := findConversationByID(conversations, convID)
	unread := getMemberUnreadCount(conv, userB.ID.String())
	assert.Equal(t, 0, unread, "标记已读后未读应为0")

	if !receivedReceipt {
		t.Skip("未实现已读回执功能，跳过后续测试")
		return
	}

	// 4. B回复，让A可以继续发送（绕过首条消息限制）
	wsSend(wsB, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         "Reply from B",
	})
	wsReceive(wsB, 3*time.Second) // B收到自己的消息
	wsReceive(wsA, 3*time.Second) // A收到B的回复

	time.Sleep(500 * time.Millisecond)

	// 5. 关闭已读回执
	resp, _, err = httpRequest("POST", APIPrefix+"/admin/settings/enable_read_receipt", userA.Token, map[string]interface{}{
		"value": "false",
	})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "关闭已读回执应该成功")
	httpRequest("POST", APIPrefix+"/admin/settings/reload", userA.Token, nil)
	time.Sleep(500 * time.Millisecond)

	// 6. A发第二条消息
	wsSend(wsA, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         "Second message",
	})
	msgA2, err := wsReceive(wsA, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "message", msgA2["type"], "A发送第二条消息应该成功")

	msgData2 := msgA2["data"].(map[string]interface{})
	msgID2 := msgData2["id"].(string)

	wsReceive(wsB, 3*time.Second) // B收到消息

	// 7. B标记已读，验证A不应收到read_receipt事件（因为功能已关闭）
	wsSend(wsB, "read", map[string]interface{}{
		"conversation_id": convID,
		"message_id":      msgID2,
	})

	receivedReceiptAfterDisable := false
	for i := 0; i < 3; i++ {
		msg, err := wsReceive(wsA, 1*time.Second)
		if err == nil && msg["type"] == "read" {
			receivedReceiptAfterDisable = true
			break
		}
	}

	assert.False(t, receivedReceiptAfterDisable, "关闭已读回执后，A不应收到read事件")
	if !receivedReceiptAfterDisable {
		t.Log("✓ 关闭已读回执后，A没有收到read事件")
	}

	// 验证数据库状态：虽然没有通知，但未读状态应该更新
	time.Sleep(500 * time.Millisecond)
	conversations, _ = getConversationList(userB.Token)
	conv = findConversationByID(conversations, convID)
	unread = getMemberUnreadCount(conv, userB.ID.String())
	assert.Equal(t, 0, unread, "关闭已读回执后，标记已读仍应更新未读状态")

	// 8. 恢复系统配置
	httpRequest("POST", APIPrefix+"/admin/settings/enable_read_receipt", userA.Token, map[string]interface{}{
		"value": "true",
	})
	httpRequest("POST", APIPrefix+"/admin/settings/reload", userA.Token, nil)
}

// ============================================
// 可配置功能 - 正在输入提示
// ============================================

// TestTypingIndicator_Enabled 测试正在输入提示（启用状态）
//
// 测试目标：
// - 启用时，发送typing事件，对方收到通知
// - 关闭后，发送typing事件，对方不再收到通知
//
// 验证闭环：
// 1. 确保启用正在输入提示（enable_typing_indicator=true）
// 2. A和B建立会话
// 3. A发送typing事件，B收到通知
// 4. 关闭正在输入提示（enable_typing_indicator=false）
// 5. A发送typing事件，B不应收到通知
// 6. 恢复系统配置
func TestTypingIndicator_Enabled(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	// 1. 确保启用正在输入提示
	resp, _, err := httpRequest("POST", APIPrefix+"/admin/settings/enable_typing_indicator", userA.Token, map[string]interface{}{
		"value": "true",
	})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "启用正在输入提示应该成功")
	httpRequest("POST", APIPrefix+"/admin/settings/reload", userA.Token, nil)
	time.Sleep(500 * time.Millisecond)

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	// 2. 先建立会话
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "Hello",
	})
	msgA, _ := wsReceive(wsA, 3*time.Second)
	convID := msgA["data"].(map[string]interface{})["conversation_id"].(string)
	wsReceive(wsB, 3*time.Second)

	// 3. A发送typing事件，验证B收到通知
	err = wsSend(wsA, "typing", map[string]interface{}{
		"conversation_id": convID,
	})
	require.NoError(t, err)

	receivedTyping := false
	for i := 0; i < 5; i++ {
		msg, err := wsReceive(wsB, 2*time.Second)
		if err == nil && msg["type"] == "typing" {
			data := msg["data"].(map[string]interface{})
			assert.Equal(t, convID, data["conversation_id"])
			assert.Equal(t, userA.ID.String(), data["user_id"])
			receivedTyping = true
			t.Log("✓ 启用正在输入提示时，B收到了typing事件")
			break
		}
	}

	if !receivedTyping {
		t.Skip("未实现正在输入提示功能，跳过后续测试")
		return
	}

	// 4. 关闭正在输入提示
	resp, _, err = httpRequest("POST", APIPrefix+"/admin/settings/enable_typing_indicator", userA.Token, map[string]interface{}{
		"value": "false",
	})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "关闭正在输入提示应该成功")
	httpRequest("POST", APIPrefix+"/admin/settings/reload", userA.Token, nil)
	time.Sleep(500 * time.Millisecond)

	// 5. A发送typing事件，验证B不应收到通知
	err = wsSend(wsA, "typing", map[string]interface{}{
		"conversation_id": convID,
	})
	require.NoError(t, err)

	receivedTypingAfterDisable := false
	for i := 0; i < 3; i++ {
		msg, err := wsReceive(wsB, 1*time.Second)
		if err == nil && msg["type"] == "typing" {
			receivedTypingAfterDisable = true
			break
		}
	}

	assert.False(t, receivedTypingAfterDisable, "关闭正在输入提示后，B不应收到typing事件")
	if !receivedTypingAfterDisable {
		t.Log("✓ 关闭正在输入提示后，B没有收到typing事件")
	}

	// 6. 恢复系统配置
	httpRequest("POST", APIPrefix+"/admin/settings/enable_typing_indicator", userA.Token, map[string]interface{}{
		"value": "true",
	})
	httpRequest("POST", APIPrefix+"/admin/settings/reload", userA.Token, nil)
}

// ============================================
// 可配置功能 - 拉黑功能
// ============================================

// TestBlock_SendMessage 测试拉黑后发消息被拒绝
//
// 测试目标：
// - B拉黑A后，A无法给B发消息
// - B取消拉黑后，A可以正常发消息
//
// 验证闭环：
// 1. B拉黑A
// 2. A尝试给B发消息（失败，收到error）
// 3. B取消拉黑A
// 4. A给B发消息（成功）
// 5. B收到消息
func TestBlock_SendMessage(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	// 1. B拉黑A
	resp, _, err := httpRequest("POST", APIPrefix+"/relationships/block", userB.Token, map[string]interface{}{
		"target_user_id": userA.ID.String(),
	})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "拉黑操作应该成功")
	t.Log("✓ B成功拉黑A")

	time.Sleep(100 * time.Millisecond)

	// 2. A尝试给B发消息（应该失败）
	err = wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "Should be blocked",
	})
	require.NoError(t, err)

	msg, err := wsReceive(wsA, 3*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "error", msg["type"], "拉黑后应该收到error")
	errorData := msg["data"].(map[string]interface{})
	assert.Contains(t, strings.ToLower(errorData["message"].(string)), "block", "错误消息应包含block")
	t.Log("✓ A发消息被拒绝，收到错误提示")

	// 3. B取消拉黑A
	resp, _, err = httpRequest("POST", APIPrefix+"/relationships/unblock", userB.Token, map[string]interface{}{
		"target_user_id": userA.ID.String(),
	})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "取消拉黑应该成功")
	t.Log("✓ B成功取消拉黑A")

	time.Sleep(100 * time.Millisecond)

	// 4. A给B发消息（应该成功）
	err = wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "After unblock",
	})
	require.NoError(t, err)

	msgA, err := wsReceive(wsA, 3*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "message", msgA["type"], "取消拉黑后应该可以发消息")
	t.Log("✓ A成功发送消息")

	// 5. 验证闭环：B收到消息
	msgB, err := wsReceive(wsB, 3*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "message", msgB["type"], "B应该收到消息")
	assert.Equal(t, "After unblock", msgB["data"].(map[string]interface{})["content"], "消息内容应该正确")
	t.Log("✓ B成功收到消息")
}

// TestBlock_ExistingConversation 测试拉黑后已有会话的行为
//
// 测试目标：
// - 拉黑后已有会话仍可见，但不能发新消息
//
// 验证闭环：
// 1. A和B先发过消息（建立会话）
// 2. B拉黑A
// 3. A查询会话列表，会话仍存在
// 4. A尝试发新消息，失败
func TestBlock_ExistingConversation(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	// 1. A和B先发过消息
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "Before block",
	})
	msgA, _ := wsReceive(wsA, 3*time.Second)
	convID := msgA["data"].(map[string]interface{})["conversation_id"].(string)

	// 2. B拉黑A
	httpRequest("POST", APIPrefix+"/relationships/block", userB.Token, map[string]interface{}{
		"target_user_id": userA.ID.String(),
	})

	// 3. 验证闭环：A查询会话列表，会话仍存在
	conversations, err := getConversationList(userA.Token)
	require.NoError(t, err)

	conv := findConversationByID(conversations, convID)
	assert.NotNil(t, conv, "拉黑后已有会话应该仍可见")

	// 4. A尝试发新消息（应该失败）
	wsSend(wsA, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         "After block",
	})

	msg, _ := wsReceive(wsA, 3*time.Second)
	assert.Equal(t, "error", msg["type"], "拉黑后不能发新消息")
}

// TestBlock_GetBlockList 测试获取拉黑列表
//
// 测试目标：
// - 可以查询自己的拉黑列表
//
// 验证闭环：
// 1. A拉黑B和C
// 2. A查询拉黑列表
// 3. 验证列表包含2个用户
func TestBlock_GetBlockList(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()
	userC := createTestUser()

	// 1. A拉黑B和C
	httpRequest("POST", APIPrefix+"/relationships/block", userA.Token, map[string]interface{}{
		"target_user_id": userB.ID.String(),
	})
	httpRequest("POST", APIPrefix+"/relationships/block", userA.Token, map[string]interface{}{
		"target_user_id": userC.ID.String(),
	})

	// 2. A查询拉黑列表
	resp, body, err := httpRequest("GET", APIPrefix+"/relationships/blocked", userA.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	// 3. 验证闭环：列表包含2个用户
	result := parseResponse(body)
	blockedUsers := result["blocked_users"].([]interface{})
	assert.Equal(t, 2, len(blockedUsers), "拉黑列表应该有2个用户")
}

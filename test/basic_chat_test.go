package test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================
// 基础功能 - 私聊
// ============================================

// TestPrivateChat_AutoCreateConversation 测试私聊自动创建会话
//
// 测试目标：
// - 用户A给用户B首次发送消息时，系统自动创建私聊会话
//
// 验证闭环：
// 1. A发送消息成功，收到带conversation_id的响应
// 2. A查询会话列表，能看到新创建的会话
// 3. 会话类型为"private"
// 4. 会话包含2个成员（A和B）
func TestPrivateChat_AutoCreateConversation(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, err := connectWebSocket(userA.Token)
	require.NoError(t, err)
	defer wsA.Close()

	// 1. A给B发送首条消息
	err = wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "Hello B!",
	})
	require.NoError(t, err)

	// 2. A收到发送成功的消息（包含conversation_id）
	msg, err := wsReceive(wsA, 3*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "message", msg["type"], "应该收到message类型响应")

	data := msg["data"].(map[string]interface{})
	conversationID := data["conversation_id"].(string)
	assert.NotEmpty(t, conversationID, "conversation_id不应为空")

	// 3. 验证闭环：A查询会话列表能看到新会话
	conversations, err := getConversationList(userA.Token)
	require.NoError(t, err)
	assert.Greater(t, len(conversations), 0, "会话列表不应为空")

	conv := findConversationByID(conversations, conversationID)
	require.NotNil(t, conv, "应该能找到新创建的会话")

	// 4. 验证会话属性
	assert.Equal(t, "private", conv["conversation_type"], "会话类型应为private")

	members := conv["members"].([]interface{})
	assert.Equal(t, 2, len(members), "私聊会话应有2个成员")
}

// TestPrivateChat_NoDuplicateConversation 测试私聊会话不重复创建
//
// 测试目标：
// - A给B发消息，B给A发消息，应该使用同一个会话
//
// 验证闭环：
// 1. A给B发消息，获得会话ID1
// 2. B给A发消息，获得会话ID2
// 3. ID1 == ID2（同一会话）
// 4. 查询会话列表，A和B都只看到1个会话
// 5. 该会话包含2条消息
func TestPrivateChat_NoDuplicateConversation(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	// 1. A给B发消息
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "A to B",
	})

	msgA, _ := wsReceive(wsA, 3*time.Second)
	convID1 := msgA["data"].(map[string]interface{})["conversation_id"].(string)

	wsReceive(wsB, 3*time.Second) // B收到消息

	// 2. B给A回消息
	wsSend(wsB, "message", map[string]interface{}{
		"receiver_id":  userA.ID.String(),
		"message_type": "text",
		"content":      "B to A",
	})

	msgB, _ := wsReceive(wsB, 3*time.Second)
	convID2 := msgB["data"].(map[string]interface{})["conversation_id"].(string)

	// 3. 验证：两个conversation_id相同
	assert.Equal(t, convID1, convID2, "应该使用同一个会话")

	// 4. 验证闭环：查询会话列表
	conversationsA, _ := getConversationList(userA.Token)
	conversationsB, _ := getConversationList(userB.Token)

	assert.Equal(t, 1, len(conversationsA), "A应该只有1个会话")
	assert.Equal(t, 1, len(conversationsB), "B应该只有1个会话")

	// 5. 验证会话包含2条消息
	messages, err := getMessages(userA.Token, convID1)
	require.NoError(t, err)
	assert.Equal(t, 2, len(messages), "会话应包含2条消息")
}

// TestPrivateChat_ConcurrentCreate 测试并发创建会话的幂等性
//
// 测试目标：
// - A和B同时首次给对方发消息，不应创建2个会话
//
// 验证闭环：
// 1. A和B并发发送消息
// 2. 两人都成功发送
// 3. 查询数据库，只有1个会话存在
// 4. 该会话包含2条消息
func TestPrivateChat_ConcurrentCreate(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	var wg sync.WaitGroup
	var convIDA, convIDB string

	// 1. 并发发送消息
	wg.Add(2)
	go func() {
		defer wg.Done()
		wsSend(wsA, "message", map[string]interface{}{
			"receiver_id":  userB.ID.String(),
			"message_type": "text",
			"content":      "A concurrent",
		})
		msg, _ := wsReceive(wsA, 3*time.Second)
		convIDA = msg["data"].(map[string]interface{})["conversation_id"].(string)
		// A还会收到B发的消息，消耗掉
		wsReceive(wsA, 3*time.Second)
	}()

	go func() {
		defer wg.Done()
		wsSend(wsB, "message", map[string]interface{}{
			"receiver_id":  userA.ID.String(),
			"message_type": "text",
			"content":      "B concurrent",
		})
		msg, _ := wsReceive(wsB, 3*time.Second)
		convIDB = msg["data"].(map[string]interface{})["conversation_id"].(string)
		// B还会收到A发的消息，消耗掉
		wsReceive(wsB, 3*time.Second)
	}()

	wg.Wait()
	time.Sleep(200 * time.Millisecond) // 等待所有消息处理完成

	// 2. 验证：两人都拿到了conversation_id
	assert.NotEmpty(t, convIDA, "A应该拿到conversation_id")
	assert.NotEmpty(t, convIDB, "B应该拿到conversation_id")

	// 3. 验证闭环：只有1个会话
	conversations, _ := getConversationList(userA.Token)
	assert.Equal(t, 1, len(conversations), "应该只创建1个会话")

	// 4. 验证包含2条消息
	convID := convIDA
	if convID == "" {
		convID = convIDB
	}
	messages, _ := getMessages(userA.Token, convID)
	assert.Equal(t, 2, len(messages), "应包含2条消息")
}

// TestPrivateChat_MessageTypes 测试不同消息类型
//
// 测试目标：
// - 支持text、image、video、emoji等消息类型
// - metadata正确存储和返回
//
// 验证闭环：
// 1. 发送text消息，验证content
// 2. 发送image消息，验证metadata（url、尺寸等）
// 3. 发送video消息，验证metadata（url、时长、封面等）
// 4. 发送emoji消息，验证metadata
// 5. 查询消息历史，所有字段完整返回
func TestPrivateChat_MessageTypes(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	var convID string

	// 1. 发送text消息
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "Text message",
	})
	msgA, _ := wsReceive(wsA, 3*time.Second)
	convID = msgA["data"].(map[string]interface{})["conversation_id"].(string)

	// B 接收消息（wsReceive 会自动跳过 unread_count_update）
	msgB, _ := wsReceive(wsB, 3*time.Second)

	data := msgB["data"].(map[string]interface{})
	assert.Equal(t, "text", data["message_type"])
	assert.Equal(t, "Text message", data["content"])

	// 2. B发送image消息
	wsSend(wsB, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "image",
		"content":         "https://example.com/image.jpg",
		"metadata": map[string]interface{}{
			"image_url":     "https://example.com/image.jpg",
			"thumbnail_url": "https://example.com/thumb.jpg",
			"width":         800,
			"height":        600,
		},
	})
	wsReceive(wsB, 3*time.Second)           // B 收到自己发的消息
	msgA, _ = wsReceive(wsA, 3*time.Second) // A 接收消息

	data = msgA["data"].(map[string]interface{})
	assert.Equal(t, "image", data["message_type"])
	if data["metadata"] != nil {
		metadata := data["metadata"].(map[string]interface{})
		assert.Equal(t, "https://example.com/image.jpg", metadata["image_url"])
		assert.Equal(t, float64(800), metadata["width"])
	} else {
		t.Fatal("image消息的metadata为nil")
	}

	// 3. 发送video消息
	wsSend(wsA, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "video",
		"content":         "https://example.com/video.mp4",
		"metadata": map[string]interface{}{
			"video_url": "https://example.com/video.mp4",
			"cover_url": "https://example.com/cover.jpg",
			"duration":  120,
			"file_size": 2 * 1024 * 1024, // 2MB
		},
	})
	wsReceive(wsA, 3*time.Second)           // A 收到自己发的消息
	msgB, _ = wsReceive(wsB, 3*time.Second) // B 接收消息

	data = msgB["data"].(map[string]interface{})
	assert.Equal(t, "video", data["message_type"])
	if data["metadata"] != nil {
		videoMetadata := data["metadata"].(map[string]interface{})
		assert.Equal(t, "https://example.com/video.mp4", videoMetadata["video_url"])
		assert.Equal(t, float64(120), videoMetadata["duration"])
	} else {
		t.Fatal("video消息的metadata为nil")
	}

	// 4. B发送emoji消息
	wsSend(wsB, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "emoji",
		"metadata": map[string]interface{}{
			"emoji_id": "emoji_smile",
		},
	})
	wsReceive(wsB, 3*time.Second)           // B 收到自己发的消息
	msgA, _ = wsReceive(wsA, 3*time.Second) // A 接收消息

	data = msgA["data"].(map[string]interface{})
	assert.Equal(t, "emoji", data["message_type"])
	if data["metadata"] != nil {
		emojiMetadata := data["metadata"].(map[string]interface{})
		assert.Equal(t, "emoji_smile", emojiMetadata["emoji_id"])
	} else {
		t.Fatal("emoji消息的metadata为nil")
	}

	// 5. 验证闭环：查询消息历史，验证所有消息都存在
	messages, _ := getMessages(userA.Token, convID)
	assert.Equal(t, 4, len(messages), "应该有4条消息（text+image+video+emoji）")

	// 验证消息类型
	types := make(map[string]int)
	for _, msg := range messages {
		m := msg.(map[string]interface{})
		msgType := m["message_type"].(string)
		types[msgType]++
	}
	assert.Equal(t, 1, types["text"])
	assert.Equal(t, 1, types["image"])
	assert.Equal(t, 1, types["video"])
	assert.Equal(t, 1, types["emoji"])
}

// TestPrivateChat_ReplyMessage 测试消息回复功能
//
// 测试目标：
// - 消息可以回复另一条消息
// - reply_to_message_id正确记录
//
// 验证闭环：
// 1. A发送原始消息
// 2. B回复该消息，带reply_to_message_id
// 3. A收到回复，验证reply_to_message_id字段
// 4. 查询消息历史，验证回复关系存在
func TestPrivateChat_ReplyMessage(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	// 1. A发送原始消息
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "Original message",
	})

	msgA, _ := wsReceive(wsA, 3*time.Second)
	originalMsgID := msgA["data"].(map[string]interface{})["id"].(string)
	convID := msgA["data"].(map[string]interface{})["conversation_id"].(string)

	wsReceive(wsB, 3*time.Second) // B收到

	// 2. B回复消息
	wsSend(wsB, "message", map[string]interface{}{
		"conversation_id":     convID,
		"message_type":        "text",
		"content":             "Reply message",
		"reply_to_message_id": originalMsgID,
	})

	wsReceive(wsB, 3*time.Second) // B收到自己发的回复

	// 3. A收到回复，验证reply_to_message_id
	msgA, _ = wsReceive(wsA, 3*time.Second)
	data := msgA["data"].(map[string]interface{})
	assert.Equal(t, originalMsgID, data["reply_to_message_id"], "reply_to_message_id应该正确")

	// 4. 验证闭环：查询消息历史
	messages, _ := getMessages(userA.Token, convID)
	assert.Equal(t, 2, len(messages), "应该有2条消息")

	// 找到回复消息，验证reply_to_message_id
	replyMsg := findMessageByID(messages, data["id"].(string))
	require.NotNil(t, replyMsg)
	assert.Equal(t, originalMsgID, replyMsg["reply_to_message_id"])
}

// ============================================
// 基础功能 - 群聊
// ============================================

// TestGroupChat_CreateAndSend 测试创建群聊并发送消息
//
// 测试目标：
// - 创建群聊成功
// - 创建者自动成为owner
// - 群内成员可以发送消息
//
// 验证闭环：
// 1. owner创建群聊，指定成员列表
// 2. 验证群聊创建成功，返回group_id
// 3. owner查询会话列表，能看到群聊
// 4. 验证owner角色为"owner"
// 5. owner发送消息，所有在线成员收到
// 6. 查询消息历史，消息存在
func TestGroupChat_CreateAndSend(t *testing.T) {
	owner := createTestUser()
	member1 := createTestUser()
	member2 := createTestUser()

	// 1. 创建群聊
	resp, body, err := httpRequest("POST", "/api/conversations/group", owner.Token, map[string]interface{}{
		"group_name": "Test Group",
		"member_ids": []string{member1.ID.String(), member2.ID.String()},
	})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	// 2. 验证返回群聊信息
	group := parseResponse(body)
	groupID := group["id"].(string)
	assert.NotEmpty(t, groupID, "group_id不应为空")
	assert.Equal(t, "group", group["conversation_type"])
	assert.Equal(t, "Test Group", group["group_name"])

	// 3. 验证闭环：owner查询会话列表
	conversations, _ := getConversationList(owner.Token)
	conv := findConversationByID(conversations, groupID)
	require.NotNil(t, conv, "owner应该能看到群聊")

	// 4. 验证owner角色
	ownerRole := getMemberRole(conv, owner.ID.String())
	assert.Equal(t, "owner", ownerRole, "创建者应该是owner")

	// 验证成员数量（owner + 2个成员 = 3人）
	members := conv["members"].([]interface{})
	assert.Equal(t, 3, len(members), "群聊应有3个成员")

	// 5. owner发送群消息
	wsOwner, _ := connectWebSocket(owner.Token)
	defer wsOwner.Close()

	wsMember1, _ := connectWebSocket(member1.Token)
	defer wsMember1.Close()

	wsSend(wsOwner, "message", map[string]interface{}{
		"conversation_id": groupID,
		"message_type":    "text",
		"content":         "Group message",
	})

	wsReceive(wsOwner, 3*time.Second) // owner收到

	// member1收到消息
	msg, err := wsReceive(wsMember1, 3*time.Second)
	require.NoError(t, err)
	data := msg["data"].(map[string]interface{})
	assert.Equal(t, groupID, data["conversation_id"])
	assert.Equal(t, "Group message", data["content"])

	// 6. 验证闭环：查询消息历史
	messages, _ := getMessages(owner.Token, groupID)
	assert.Equal(t, 1, len(messages), "应该有1条群消息")
}

// TestGroupChat_MemberLeave 测试群成员离开
//
// 测试目标：
// - 群成员可以主动离开群聊
// - 离开后无法查看群消息
// - 离开后群不出现在会话列表
//
// 验证闭环：
// 1. 创建群聊
// 2. member1离开群聊
// 3. 验证离开成功（返回200）
// 4. member1查询会话列表，群聊不存在
// 5. member1尝试查询群消息，返回403/404
func TestGroupChat_MemberLeave(t *testing.T) {
	owner := createTestUser()
	member1 := createTestUser()

	// 1. 创建群聊
	resp, body, _ := httpRequest("POST", "/api/conversations/group", owner.Token, map[string]interface{}{
		"group_name": "Test Group",
		"member_ids": []string{member1.ID.String()},
	})
	group := parseResponse(body)
	groupID := group["id"].(string)

	// 2. member1离开群聊
	resp, _, err := httpRequest("POST", "/api/conversations/"+groupID+"/leave", member1.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "离开群聊应该成功")

	// 3. 验证闭环：member1查询会话列表，群聊不存在
	conversations, _ := getConversationList(member1.Token)
	conv := findConversationByID(conversations, groupID)
	assert.Nil(t, conv, "离开的群聊不应出现在会话列表")

	// 4. member1尝试查询群消息，应该失败
	resp, _, _ = httpRequest("GET", "/api/conversations/"+groupID+"/messages", member1.Token, nil)
	assert.True(t, resp.StatusCode == 403 || resp.StatusCode == 404, "离开后不应能查看群消息")
}

// ============================================
// 基础功能 - 未读计数
// ============================================

// TestUnreadCount_IncrementAndReset 测试未读计数增加和清零
//
// 测试目标：
// - 收到消息时未读计数+1
// - 标记已读后未读计数归零
// - 自己发的消息不增加自己的未读
//
// 验证闭环：
// 1. A给B发3条消息
// 2. B查询会话列表，验证未读计数=3
// 3. B标记已读
// 4. B再次查询会话列表，验证未读计数=0
// 5. A查询会话列表，验证自己的未读=0
func TestUnreadCount_IncrementAndReset(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	var convID, lastMsgID string

	// 连接 B 的 WebSocket（用于接收消息和回复）
	wsB, _ := connectWebSocket(userB.Token)
	defer wsB.Close()

	// 1. A发送第一条消息
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "Message 0",
	})
	msg, _ := wsReceive(wsA, 3*time.Second)
	convID = msg["data"].(map[string]interface{})["conversation_id"].(string)
	lastMsgID = msg["data"].(map[string]interface{})["id"].(string)
	wsReceive(wsB, 3*time.Second) // B 收到第一条

	// 2. B 回复一条消息（解除首条消息限制）
	wsSend(wsB, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         "Reply from B",
	})
	wsReceive(wsB, 3*time.Second) // B 收到自己的消息
	wsReceive(wsA, 3*time.Second) // A 收到 B 的回复

	// 3. A 继续发送第2、3条消息
	for i := 1; i < 3; i++ {
		wsSend(wsA, "message", map[string]interface{}{
			"conversation_id": convID,
			"message_type":    "text",
			"content":         fmt.Sprintf("Message %d", i),
		})
		msg, _ := wsReceive(wsA, 3*time.Second)
		lastMsgID = msg["data"].(map[string]interface{})["id"].(string)
		wsReceive(wsB, 3*time.Second) // B 收到消息
		time.Sleep(100 * time.Millisecond)
	}

	// 4. 验证闭环：B查询会话列表，未读计数=3（A的3条消息）
	conversationsB, _ := getConversationList(userB.Token)
	convB := findConversationByID(conversationsB, convID)
	require.NotNil(t, convB)

	unreadB := getMemberUnreadCount(convB, userB.ID.String())
	assert.Equal(t, 3, unreadB, "B应该有3条未读")

	// 5. B标记已读
	wsSend(wsB, "read", map[string]interface{}{
		"conversation_id": convID,
		"message_id":      lastMsgID,
	})

	time.Sleep(500 * time.Millisecond)

	// 6. 验证闭环：B再次查询，未读计数=0
	conversationsB, _ = getConversationList(userB.Token)
	convB = findConversationByID(conversationsB, convID)
	unreadB = getMemberUnreadCount(convB, userB.ID.String())
	assert.Equal(t, 0, unreadB, "标记已读后未读应为0")

	// 7. 验证：A的未读计数=1（B的回复）
	conversationsA, _ := getConversationList(userA.Token)
	convA := findConversationByID(conversationsA, convID)
	unreadA := getMemberUnreadCount(convA, userA.ID.String())
	assert.Equal(t, 1, unreadA, "A应该有1条未读（B的回复）")
}

// TestUnreadCount_GroupMultipleSenders 测试群聊多人发送的未读计数
//
// 测试目标：
// - 群聊中多人发消息，离线成员未读正确累加
//
// 验证闭环：
// 1. 创建群聊（owner + member1 + member2 + member3）
// 2. owner发2条消息，member1发1条消息
// 3. member3完全离线，查询会话列表
// 4. 验证member3的未读计数=3
func TestUnreadCount_GroupMultipleSenders(t *testing.T) {
	owner := createTestUser()
	member1 := createTestUser()
	member2 := createTestUser()
	member3 := createTestUser()

	// 1. 创建群聊
	_, body, _ := httpRequest("POST", "/api/conversations/group", owner.Token, map[string]interface{}{
		"group_name": "Test Group",
		"member_ids": []string{member1.ID.String(), member2.ID.String(), member3.ID.String()},
	})
	group := parseResponse(body)
	groupID := group["id"].(string)

	// 2. owner和member1发送消息
	wsOwner, _ := connectWebSocket(owner.Token)
	defer wsOwner.Close()

	wsMember1, _ := connectWebSocket(member1.Token)
	defer wsMember1.Close()

	// owner发2条
	for i := 0; i < 2; i++ {
		wsSend(wsOwner, "message", map[string]interface{}{
			"conversation_id": groupID,
			"message_type":    "text",
			"content":         fmt.Sprintf("Owner message %d", i),
		})
		wsReceive(wsOwner, 3*time.Second)
		time.Sleep(100 * time.Millisecond)
	}

	// member1发1条
	wsSend(wsMember1, "message", map[string]interface{}{
		"conversation_id": groupID,
		"message_type":    "text",
		"content":         "Member1 message",
	})
	wsReceive(wsMember1, 3*time.Second)

	time.Sleep(500 * time.Millisecond)

	// 3. 验证闭环：member3查询会话列表，未读=3
	conversations, _ := getConversationList(member3.Token)
	conv := findConversationByID(conversations, groupID)
	require.NotNil(t, conv)

	unread := getMemberUnreadCount(conv, member3.ID.String())
	assert.Equal(t, 3, unread, "member3应该有3条未读（2条owner+1条member1）")
}

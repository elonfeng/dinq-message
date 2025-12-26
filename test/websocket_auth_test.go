package test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================
// WebSocket 连接
// ============================================

// TestWebSocket_ConnectAndDisconnect 测试WebSocket连接和断开
//
// 测试目标：
// - 带有效token可以成功连接WebSocket
// - 可以正常断开连接
// - 断开后发送消息失败
//
// 验证闭环：
// 1. 连接WebSocket成功
// 2. 主动断开连接
// 3. 尝试发送消息，验证失败
func TestWebSocket_ConnectAndDisconnect(t *testing.T) {
	user := createTestUser()

	// 1. 连接WebSocket
	ws, err := connectWebSocket(user.Token)
	require.NoError(t, err, "应该能成功连接WebSocket")
	assert.NotNil(t, ws)

	// 2. 断开连接
	err = ws.Close()
	require.NoError(t, err, "断开连接应该成功")

	// 3. 验证闭环：断开后发送消息失败
	err = wsSend(ws, "message", map[string]interface{}{
		"receiver_id":  uuid.New().String(),
		"message_type": "text",
		"content":      "Should fail",
	})
	assert.Error(t, err, "断开后发送消息应该失败")
}

// TestWebSocket_Heartbeat 测试心跳机制
//
// 测试目标：
// - WebSocket支持heartbeat心跳
// - 发送heartbeat可以刷新Redis在线状态
//
// 验证闭环：
// 1. 连接WebSocket，用户上线
// 2. 验证Redis中有在线状态
// 3. 发送heartbeat消息
// 4. 验证Redis中的在线状态被刷新（TTL更新）
func TestWebSocket_Heartbeat(t *testing.T) {
	user := createTestUser()
	rdb := getRedisClient()
	defer rdb.Close()
	ctx := context.Background()

	ws, _ := connectWebSocket(user.Token)
	defer ws.Close()

	// 1. 轮询等待连接建立，验证Redis中有在线状态（最多等待2秒）
	onlineKey := "online:" + user.ID.String()
	online := false
	for i := 0; i < 20; i++ {
		val, err := rdb.Get(ctx, onlineKey).Result()
		if err == nil && val == "1" {
			online = true
			t.Logf("连接建立成功，耗时 %d ms", i*100)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.True(t, online, "连接后应该有在线状态")

	val, err := rdb.Get(ctx, onlineKey).Result()
	require.NoError(t, err)
	assert.Equal(t, "1", val)

	// 2. 获取初始TTL
	ttl1, err := rdb.TTL(ctx, onlineKey).Result()
	require.NoError(t, err)
	t.Logf("初始TTL: %v", ttl1)

	// 3. 等待几秒让TTL减少
	time.Sleep(3 * time.Second)
	ttl2, err := rdb.TTL(ctx, onlineKey).Result()
	require.NoError(t, err)
	t.Logf("等待后TTL: %v", ttl2)
	assert.Less(t, ttl2, ttl1, "TTL应该减少")

	// 4. 发送heartbeat
	err = wsSend(ws, "heartbeat", nil)
	require.NoError(t, err, "发送heartbeat应该成功")

	// 5. 验证闭环：TTL被刷新，应该接近30秒
	time.Sleep(500 * time.Millisecond)
	ttl3, err := rdb.TTL(ctx, onlineKey).Result()
	require.NoError(t, err)
	t.Logf("心跳后TTL: %v", ttl3)
	assert.Greater(t, ttl3, ttl2, "心跳后TTL应该被刷新")
	assert.GreaterOrEqual(t, ttl3.Seconds(), 25.0, "TTL应该接近30秒")

	t.Log("心跳机制测试通过：Redis在线状态正常刷新")
}

// TestWebSocket_OnlineStatus 测试在线状态查询（被动模型）
//
// 测试目标：
// - 私聊会话：进入聊天时返回对方的在线状态
// - 群聊会话：不返回在线状态（性能优化）
// - 在线状态准确性：在线/离线判断正确
//
// 验证闭环：
// 1. A和B建立私聊会话
// 2. A在线，B查询消息列表，应该返回A的在线状态为true
// 3. A断开连接（离线）
// 4. B再次查询消息列表，应该返回A的在线状态为false
// 5. 测试群聊：创建群聊，查询消息应该不返回在线状态
func TestWebSocket_OnlineStatus(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()
	userC := createTestUser()

	// 1. A和B建立私聊会话
	wsA, err := connectWebSocket(userA.Token)
	require.NoError(t, err)
	// 注意：不能defer，因为测试中间需要断开连接

	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "Hello",
	})
	msg, err := wsReceive(wsA, 3*time.Second)
	require.NoError(t, err)
	require.NotNil(t, msg, "消息不应为nil")
	t.Logf("收到的WebSocket消息: %+v", msg)

	time.Sleep(500 * time.Millisecond) // 等待消息保存

	// 获取会话ID
	data, ok := msg["data"].(map[string]interface{})
	require.True(t, ok, "消息应该包含data字段，实际收到: %+v", msg)
	t.Logf("data内容: %+v", data)

	conversationID, ok := data["conversation_id"].(string)
	require.True(t, ok, "data应该包含conversation_id字段，data内容: %+v", data)
	require.NotEmpty(t, conversationID, "conversation_id不应为空")

	// 2. B查询消息列表，A在线
	resp, body, err := httpRequest("GET", APIPrefix+"/conversations/"+conversationID+"/messages?limit=50", userB.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "查询消息应该成功")

	result := parseResponse(body)

	// 验证统一响应格式（在 parseResponse 中已解析 data）
	// parseResponse 返回的是 data 字段的内容

	onlineStatus, ok := result["online_status"].(map[string]interface{})
	require.True(t, ok, "私聊应该返回online_status字段")

	// 验证只包含对方，不包含自己
	assert.Equal(t, 1, len(onlineStatus), "私聊应该只返回对方的在线状态")

	// 验证A在线
	aOnline, exists := onlineStatus[userA.ID.String()]
	assert.True(t, exists, "应该返回A的在线状态")
	assert.Equal(t, true, aOnline, "A应该是在线状态")

	// 验证不包含B自己的状态
	_, bExists := onlineStatus[userB.ID.String()]
	assert.False(t, bExists, "不应该包含B自己的在线状态")

	t.Logf("✓ 私聊会话返回对方在线状态：%v (不包含自己)", aOnline)

	// 3. A断开连接
	wsA.Close()

	// 轮询等待 Redis 状态更新（最多等待 3 秒）
	rdb := getRedisClient()
	defer rdb.Close()
	ctx := context.Background()
	onlineKey := "online:" + userA.ID.String()

	deleted := false
	for i := 0; i < 30; i++ {
		_, err := rdb.Get(ctx, onlineKey).Result()
		if err != nil { // key 不存在，表示已删除
			deleted = true
			t.Logf("Redis key 删除成功，耗时 %d ms", i*100)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.True(t, deleted, "A断开连接后，Redis中的在线状态应该被删除")

	// 4. B再次查询，A应该离线
	resp2, body2, err := httpRequest("GET", APIPrefix+"/conversations/"+conversationID+"/messages?limit=50", userB.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp2.StatusCode)

	result2 := parseResponse(body2)
	onlineStatus2 := result2["online_status"].(map[string]interface{})

	// 验证只返回 A 的状态（B 自己的状态不返回）
	assert.Equal(t, 1, len(onlineStatus2), "应该只返回对方的状态")

	aOnline2 := onlineStatus2[userA.ID.String()]
	assert.Equal(t, false, aOnline2, "A断开后应该是离线状态")
	t.Logf("✓ A断开连接后，在线状态更新为：%v (Redis key已删除)", aOnline2)

	// 5. 测试群聊：不返回在线状态
	// 创建群聊
	groupResp, groupBody, err := httpRequest("POST", APIPrefix+"/conversations/group", userA.Token, map[string]interface{}{
		"group_name": "测试群聊",
		"member_ids": []string{userB.ID.String(), userC.ID.String()},
	})
	require.NoError(t, err)
	assert.Equal(t, 200, groupResp.StatusCode, "创建群聊应该返回200")

	groupResult := parseResponse(groupBody)
	groupID := groupResult["id"].(string)

	// 连接A和C
	wsA2, _ := connectWebSocket(userA.Token)
	defer wsA2.Close()
	wsC, _ := connectWebSocket(userC.Token)
	defer wsC.Close()

	// B查询群聊消息
	groupMsgResp, groupMsgBody, err := httpRequest("GET", APIPrefix+"/conversations/"+groupID+"/messages?limit=50", userB.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, groupMsgResp.StatusCode)

	groupMsgResult := parseResponse(groupMsgBody)
	groupOnlineStatus, ok := groupMsgResult["online_status"].(map[string]interface{})
	require.True(t, ok, "群聊也应该返回online_status字段")
	assert.Empty(t, groupOnlineStatus, "群聊的online_status应该为空（性能优化）")
	t.Logf("✓ 群聊会话不返回在线状态（空map）：%v", groupOnlineStatus)

	t.Log("在线状态查询测试通过：私聊返回对方状态，群聊不返回")
}

// ============================================
// 鉴权测试
// ============================================

// TestAuth_NoToken 测试无token连接
//
// 测试目标：
// - 没有token无法连接WebSocket
// - 返回401状态码
//
// 验证闭环：
// 1. 不带token连接WebSocket
// 2. 连接失败，返回401
func TestAuth_NoToken(t *testing.T) {
	url := fmt.Sprintf("%s/ws", WSURL)

	// 1. 不带token连接
	_, resp, err := websocket.DefaultDialer.Dial(url, nil)

	// 2. 验证闭环：连接失败
	assert.Error(t, err, "不带token应该连接失败")

	if resp != nil {
		assert.Equal(t, 401, resp.StatusCode, "应该返回401状态码")
	}
}

// TestAuth_InvalidToken 测试无效token
//
// 测试目标：
// - 无效的token无法连接WebSocket
// - 返回401状态码
//
// 验证闭环：
// 1. 使用无效token连接
// 2. 连接失败，返回401
func TestAuth_InvalidToken(t *testing.T) {
	url := fmt.Sprintf("%s/ws?token=invalid_token_xxx", WSURL)

	// 1. 使用无效token连接
	_, resp, err := websocket.DefaultDialer.Dial(url, nil)

	// 2. 验证闭环：连接失败
	assert.Error(t, err, "无效token应该连接失败")

	if resp != nil {
		assert.Equal(t, 401, resp.StatusCode, "应该返回401状态码")
	}
}

// TestAuth_ExpiredToken 测试过期token
//
// 测试目标：
// - 过期的token无法连接WebSocket
// - 返回401状态码
//
// 验证闭环：
// 1. 创建已过期的token（exp设置为过去）
// 2. 使用过期token连接
// 3. 连接失败，返回401
func TestAuth_ExpiredToken(t *testing.T) {
	userID := uuid.New()

	// 1. 创建过期token（1小时前过期）
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": userID.String(),
		"exp":     time.Now().Add(-1 * time.Hour).Unix(),
	})
	expiredToken, _ := token.SignedString([]byte(JWTSecret))

	// 2. 使用过期token连接
	url := fmt.Sprintf("%s/ws?token=%s", WSURL, expiredToken)
	_, resp, err := websocket.DefaultDialer.Dial(url, nil)

	// 3. 验证闭环：连接失败
	assert.Error(t, err, "过期token应该连接失败")

	if resp != nil {
		assert.Equal(t, 401, resp.StatusCode, "应该返回401状态码")
	}
}

// TestAuth_HTTPEndpoint 测试HTTP接口鉴权
//
// 测试目标：
// - HTTP接口需要Authorization header
// - 无token返回401
//
// 验证闭环：
// 1. 不带token访问需要鉴权的接口
// 2. 返回401状态码
func TestAuth_HTTPEndpoint(t *testing.T) {
	// 1. 不带token访问接口
	resp, _, err := httpRequest("GET", APIPrefix+"/conversations", "", nil)

	// 2. 验证闭环：返回401
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode, "无token应该返回401")
}

// TestAuth_ValidToken 测试有效token
//
// 测试目标：
// - 有效token可以访问所有接口
// - WebSocket和HTTP都正常工作
//
// 验证闭环：
// 1. 创建有效token
// 2. 连接WebSocket成功
// 3. 访问HTTP接口成功（返回200）
func TestAuth_ValidToken(t *testing.T) {
	user := createTestUser()

	// 1. 验证WebSocket连接
	ws, err := connectWebSocket(user.Token)
	require.NoError(t, err, "有效token应该能连接WebSocket")
	defer ws.Close()

	// 2. 验证闭环：HTTP接口访问
	resp, _, err := httpRequest("GET", APIPrefix+"/conversations", user.Token, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "有效token应该能访问HTTP接口")
}

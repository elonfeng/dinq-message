package test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================
// 性能测试 - N+1查询验证
// ============================================

// TestPerformance_NoPlusOneQuery 测试N+1查询已修复
//
// 测试目标：
// - 查询会话列表不存在N+1查询问题
// - 查询时间在合理范围内（<2秒）
//
// 验证闭环：
// 1. 创建10个会话
// 2. 查询会话列表
// 3. 验证响应时间<2秒
// 4. 验证返回10个会话（含成员信息）
func TestPerformance_NoPlusOneQuery(t *testing.T) {
	user := createTestUser()

	// 1. 创建10个会话
	ws, _ := connectWebSocket(user.Token)
	defer ws.Close()

	for i := 0; i < 10; i++ {
		targetUser := createTestUser()
		wsSend(ws, "message", map[string]interface{}{
			"receiver_id":  targetUser.ID.String(),
			"message_type": "text",
			"content":      fmt.Sprintf("Conversation %d", i),
		})
		wsReceive(ws, 3*time.Second)
		time.Sleep(50 * time.Millisecond)
	}

	// 2. 查询会话列表并测量时间
	start := time.Now()
	resp, body, err := httpRequest("GET", "/api/conversations?limit=20", user.Token, nil)
	duration := time.Since(start)

	// 3. 验证闭环：响应成功且时间合理
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Less(t, duration, 2*time.Second, "查询应在2秒内完成（验证无N+1问题）")

	// 4. 验证返回数据完整
	result := parseResponse(body)
	conversations := result["conversations"].([]interface{})
	assert.GreaterOrEqual(t, len(conversations), 10, "应该返回至少10个会话")

	// 验证每个会话都包含成员信息（说明是一次性查出来的）
	for _, conv := range conversations {
		c := conv.(map[string]interface{})
		members, ok := c["members"]
		assert.True(t, ok, "会话应包含members字段")
		assert.NotNil(t, members, "members不应为nil")
	}

	t.Logf("查询10个会话耗时: %v", duration)
}

// ============================================
// 性能测试 - 并发场景
// ============================================

// TestPerformance_ConcurrentMessages 测试并发发送消息
//
// 测试目标：
// - 系统能处理高并发消息发送（真实场景：100对用户同时聊天）
// - 成功率=100%
// - 总耗时<30秒
//
// 验证闭环：
// 1. 创建100对用户（200个用户）
// 2. 每对用户建立WebSocket连接并互相发送消息
// 3. 统计成功数量和总耗时
// 4. 验证成功率=100%，耗时<30秒
func TestPerformance_ConcurrentMessages(t *testing.T) {
	concurrency := 100 // 100对用户
	var wg sync.WaitGroup
	var mu sync.Mutex
	successCount := 0
	errors := make([]string, 0)

	start := time.Now()

	// 100对用户并发聊天
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// 创建一对用户
			userA := createTestUser()
			userB := createTestUser()

			// 建立WebSocket连接
			wsA, err := connectWebSocket(userA.Token)
			if err != nil {
				mu.Lock()
				errors = append(errors, fmt.Sprintf("[%d] A连接失败: %v", idx, err))
				mu.Unlock()
				return
			}
			defer wsA.Close()

			wsB, err := connectWebSocket(userB.Token)
			if err != nil {
				mu.Lock()
				errors = append(errors, fmt.Sprintf("[%d] B连接失败: %v", idx, err))
				mu.Unlock()
				return
			}
			defer wsB.Close()

			// A给B发消息
			err = wsSend(wsA, "message", map[string]interface{}{
				"receiver_id":  userB.ID.String(),
				"message_type": "text",
				"content":      fmt.Sprintf("Hello from A-%d", idx),
			})
			if err != nil {
				mu.Lock()
				errors = append(errors, fmt.Sprintf("[%d] A发送失败: %v", idx, err))
				mu.Unlock()
				return
			}

			// A接收确认
			_, err = wsReceive(wsA, 5*time.Second)
			if err != nil {
				mu.Lock()
				errors = append(errors, fmt.Sprintf("[%d] A接收确认失败: %v", idx, err))
				mu.Unlock()
				return
			}

			// B接收消息
			msgB, err := wsReceive(wsB, 5*time.Second)
			if err != nil {
				mu.Lock()
				errors = append(errors, fmt.Sprintf("[%d] B接收消息失败: %v", idx, err))
				mu.Unlock()
				return
			}

			// B回复
			convID := msgB["data"].(map[string]interface{})["conversation_id"].(string)
			err = wsSend(wsB, "message", map[string]interface{}{
				"conversation_id": convID,
				"message_type":    "text",
				"content":         fmt.Sprintf("Reply from B-%d", idx),
			})
			if err != nil {
				mu.Lock()
				errors = append(errors, fmt.Sprintf("[%d] B回复失败: %v", idx, err))
				mu.Unlock()
				return
			}

			// B接收确认
			_, err = wsReceive(wsB, 5*time.Second)
			if err != nil {
				mu.Lock()
				errors = append(errors, fmt.Sprintf("[%d] B接收回复确认失败: %v", idx, err))
				mu.Unlock()
				return
			}

			// A接收B的回复
			_, err = wsReceive(wsA, 5*time.Second)
			if err != nil {
				mu.Lock()
				errors = append(errors, fmt.Sprintf("[%d] A接收回复失败: %v", idx, err))
				mu.Unlock()
				return
			}

			mu.Lock()
			successCount++
			mu.Unlock()
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	// 验证闭环：成功率和性能
	successRate := (successCount * 100) / concurrency
	t.Logf("并发测试: 成功%d/%d (%d%%), 耗时%v", successCount, concurrency, successRate, duration)

	// 输出前10个错误帮助诊断
	if len(errors) > 0 {
		t.Logf("失败详情（前10个）:")
		for i := 0; i < len(errors) && i < 10; i++ {
			t.Logf("  %s", errors[i])
		}
	}

	require.Equal(t, concurrency, successCount, "所有并发消息都应该成功发送")
	assert.Less(t, duration, 30*time.Second, "总耗时应<30秒")
}

// ============================================
// 性能测试 - 事务一致性
// ============================================

// TestPerformance_TransactionConsistency 测试事务一致性
//
// 测试目标：
// - 验证消息发送的事务保护
// - 消息表和会话表数据一致
//
// 验证闭环：
// 1. 发送10条消息
// 2. 查询消息历史，验证有10+1条消息
// 3. 查询会话，验证last_message_id和last_message_at正确
// 4. 验证未读计数正确
func TestPerformance_TransactionConsistency(t *testing.T) {
	userA := createTestUser()
	userB := createTestUser()

	wsA, _ := connectWebSocket(userA.Token)
	defer wsA.Close()

	// 1. 先建立会话，A发送init消息
	wsSend(wsA, "message", map[string]interface{}{
		"receiver_id":  userB.ID.String(),
		"message_type": "text",
		"content":      "Init",
	})
	msgA, _ := wsReceive(wsA, 3*time.Second)
	convID := msgA["data"].(map[string]interface{})["conversation_id"].(string)

	// 2. B上线，接收消息并回复，解除首条消息限制
	wsB, _ := connectWebSocket(userB.Token)

	// B接收init消息
	for i := 0; i < 5; i++ {
		msg, err := wsReceive(wsB, 1*time.Second)
		if err == nil {
			msgType := msg["type"].(string)
			if msgType == "message" || msgType == "offline_message" {
				break
			}
		}
	}

	// B回复，解除首条消息限制
	wsSend(wsB, "message", map[string]interface{}{
		"conversation_id": convID,
		"message_type":    "text",
		"content":         "Reply from B",
	})
	wsReceive(wsB, 3*time.Second) // B接收自己的消息
	wsReceive(wsA, 3*time.Second) // A接收B的回复

	// B下线，避免干扰后续测试
	wsB.Close()
	time.Sleep(200 * time.Millisecond)

	// 3. 发送10条消息
	var lastMsgID string
	for i := 0; i < 10; i++ {
		wsSend(wsA, "message", map[string]interface{}{
			"conversation_id": convID,
			"message_type":    "text",
			"content":         fmt.Sprintf("Message %d", i),
		})

		// 接收消息确认，跳过非消息类型
		for j := 0; j < 5; j++ {
			msg, err := wsReceive(wsA, 3*time.Second)
			if err != nil {
				break
			}
			msgType := msg["type"].(string)
			if msgType == "message" {
				lastMsgID = msg["data"].(map[string]interface{})["id"].(string)
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	time.Sleep(1 * time.Second) // 等待数据库事务提交

	// 4. 验证闭环：查询消息历史
	messages, err := getMessages(userA.Token, convID)
	require.NoError(t, err)
	assert.Equal(t, 12, len(messages), "应该有12条消息（1条init + 1条B的回复 + 10条A的消息）")

	// 5. 验证会话状态一致性
	conversations, _ := getConversationList(userA.Token)
	conv := findConversationByID(conversations, convID)
	require.NotNil(t, conv)

	// 打印调试信息
	t.Logf("lastMsgID (A发送的最后一条): %s", lastMsgID)
	t.Logf("会话的 last_message_id: %s", conv["last_message_id"])
	if len(messages) > 0 {
		lastMsg := messages[len(messages)-1].(map[string]interface{})
		t.Logf("消息历史中的最后一条ID: %s", lastMsg["id"])
		t.Logf("消息历史中的最后一条内容: %s", lastMsg["content"])
	}

	// 验证last_message_id（应该是消息历史中最后一条的ID）
	if len(messages) > 0 {
		lastMsg := messages[len(messages)-1].(map[string]interface{})
		actualLastMsgID := lastMsg["id"].(string)
		assert.Equal(t, actualLastMsgID, conv["last_message_id"], "会话的last_message_id应该是消息历史中最后一条")
	}

	// 验证last_message_at存在
	assert.NotNil(t, conv["last_message_at"], "会话的last_message_at应该存在")

	// 6. 验证未读计数（B的未读应该是11条：1条init + 10条A的消息）
	conversationsB, _ := getConversationList(userB.Token)
	convB := findConversationByID(conversationsB, convID)
	require.NotNil(t, convB)

	unreadB := getMemberUnreadCount(convB, userB.ID.String())
	assert.Equal(t, 11, unreadB, "B的未读计数应该是11")
}

// ============================================
// 性能测试 - 索引效率
// ============================================

// TestPerformance_IndexEfficiency 测试索引效率
//
// 测试目标：
// - 验证数据库索引有效
// - 查询性能符合预期
//
// 验证闭环：
// 1. 创建50个会话
// 2. 每个会话发送10条消息
// 3. 查询会话列表（带分页），验证响应时间
// 4. 查询单个会话的消息历史，验证响应时间
func TestPerformance_IndexEfficiency(t *testing.T) {
	user := createTestUser()

	ws, _ := connectWebSocket(user.Token)
	defer ws.Close()

	var testConvID string

	// 1. 创建50个会话，每个会话10条消息
	t.Log("创建测试数据...")
	for i := 0; i < 50; i++ {
		targetUser := createTestUser()

		// 发送10条消息
		for j := 0; j < 10; j++ {
			wsSend(ws, "message", map[string]interface{}{
				"receiver_id":  targetUser.ID.String(),
				"message_type": "text",
				"content":      fmt.Sprintf("Conv%d Msg%d", i, j),
			})
			msg, _ := wsReceive(ws, 3*time.Second)

			if i == 0 && j == 0 {
				testConvID = msg["data"].(map[string]interface{})["conversation_id"].(string)
			}

			time.Sleep(10 * time.Millisecond)
		}
	}

	t.Log("测试数据创建完成")

	// 2. 测试会话列表查询性能
	start := time.Now()
	resp, _, err := httpRequest("GET", "/api/conversations?limit=20&offset=0", user.Token, nil)
	duration := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Less(t, duration, 1*time.Second, "会话列表查询应<1秒（验证idx_member_user_active有效）")
	t.Logf("会话列表查询耗时: %v", duration)

	// 3. 测试消息历史查询性能
	start = time.Now()
	resp, _, err = httpRequest("GET", "/api/conversations/"+testConvID+"/messages?limit=50", user.Token, nil)
	duration = time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Less(t, duration, 500*time.Millisecond, "消息历史查询应<500ms（验证idx_msg_conversation有效）")
	t.Logf("消息历史查询耗时: %v", duration)
}

// ============================================
// 性能测试 - 真实压力测试
// ============================================

// TestPerformance_WebSocketCapacity 测试 WebSocket 连接容量
//
// 测试目标：
// - 测试系统能同时维持多少 WebSocket 连接
// - 测试在线用户消息收发性能
//
// 验证闭环：
// 1. 建立 500 个 WebSocket 连接（模拟 500 在线用户）
// 2. 随机发送消息，验证消息送达率
// 3. 统计成功率、平均延迟、P95延迟
func TestPerformance_WebSocketCapacity(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过压力测试（使用 -short 标志）")
	}

	concurrentUsers := 500
	messagesPerUser := 5

	users := make([]*TestUser, concurrentUsers)
	connections := make([]*websocket.Conn, concurrentUsers)

	// 1. 创建用户并建立连接
	t.Logf("建立 %d 个 WebSocket 连接...", concurrentUsers)
	start := time.Now()

	var wg sync.WaitGroup
	var mu sync.Mutex
	successfulConnections := 0

	for i := 0; i < concurrentUsers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			user := createTestUser()
			ws, err := connectWebSocket(user.Token)
			if err != nil {
				t.Logf("连接失败 [%d]: %v", idx, err)
				return
			}

			mu.Lock()
			users[idx] = user
			connections[idx] = ws
			successfulConnections++
			mu.Unlock()
		}(i)
	}

	wg.Wait()
	connectionDuration := time.Since(start)

	t.Logf("✓ 成功建立 %d/%d 连接，耗时 %v", successfulConnections, concurrentUsers, connectionDuration)
	assert.GreaterOrEqual(t, successfulConnections, concurrentUsers*90/100, "连接成功率应 >= 90%")

	// 2. 随机发送消息并测量延迟
	t.Log("开始发送消息...")
	type MessageResult struct {
		Success bool
		Latency time.Duration
	}

	results := make([]MessageResult, 0, successfulConnections*messagesPerUser)
	var resultsMu sync.Mutex

	start = time.Now()

	for i := 0; i < successfulConnections; i++ {
		if connections[i] == nil {
			continue
		}

		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			for j := 0; j < messagesPerUser; j++ {
				// 随机选择一个接收者
				receiverIdx := (idx + j + 1) % successfulConnections
				if users[receiverIdx] == nil {
					continue
				}

				msgStart := time.Now()
				err := wsSend(connections[idx], "message", map[string]interface{}{
					"receiver_id":  users[receiverIdx].ID.String(),
					"message_type": "text",
					"content":      fmt.Sprintf("Load test msg from %d to %d", idx, receiverIdx),
				})

				result := MessageResult{
					Success: err == nil,
					Latency: time.Since(msgStart),
				}

				if err == nil {
					_, err = wsReceive(connections[idx], 5*time.Second)
					result.Success = err == nil
					result.Latency = time.Since(msgStart)
				}

				resultsMu.Lock()
				results = append(results, result)
				resultsMu.Unlock()

				time.Sleep(10 * time.Millisecond) // 避免过快
			}
		}(i)
	}

	wg.Wait()
	totalDuration := time.Since(start)

	// 3. 统计结果
	successCount := 0
	var totalLatency time.Duration
	latencies := make([]time.Duration, 0, len(results))

	for _, r := range results {
		if r.Success {
			successCount++
			totalLatency += r.Latency
			latencies = append(latencies, r.Latency)
		}
	}

	// 计算 P95 延迟
	var p95Latency time.Duration
	if len(latencies) > 0 {
		// 简单排序
		for i := 0; i < len(latencies)-1; i++ {
			for j := i + 1; j < len(latencies); j++ {
				if latencies[i] > latencies[j] {
					latencies[i], latencies[j] = latencies[j], latencies[i]
				}
			}
		}
		p95Index := len(latencies) * 95 / 100
		p95Latency = latencies[p95Index]
	}

	avgLatency := time.Duration(0)
	if successCount > 0 {
		avgLatency = totalLatency / time.Duration(successCount)
	}

	successRate := (successCount * 100) / len(results)
	qps := float64(successCount) / totalDuration.Seconds()

	// 4. 输出性能报告
	t.Log("========================================")
	t.Log("性能测试报告")
	t.Log("========================================")
	t.Logf("并发用户数: %d", successfulConnections)
	t.Logf("总消息数: %d", len(results))
	t.Logf("成功消息数: %d (%.1f%%)", successCount, float64(successRate))
	t.Logf("总耗时: %v", totalDuration)
	t.Logf("QPS: %.2f 消息/秒", qps)
	t.Logf("平均延迟: %v", avgLatency)
	t.Logf("P95 延迟: %v", p95Latency)
	t.Log("========================================")

	// 5. 断言性能指标
	assert.GreaterOrEqual(t, successRate, 80, "消息成功率应 >= 80%")
	assert.Less(t, avgLatency, 1*time.Second, "平均延迟应 < 1秒")
	assert.Less(t, p95Latency, 3*time.Second, "P95 延迟应 < 3秒")

	// 6. 清理连接
	for _, ws := range connections {
		if ws != nil {
			ws.Close()
		}
	}
}

// TestPerformance_HighThroughput 测试高吞吐量场景
//
// 测试目标：
// - 测试系统最大消息吞吐量（TPS）
// - 测试在高负载下的稳定性
//
// 验证闭环：
// 1. 创建 100 个用户
// 2. 每个用户快速发送 50 条消息（总共 5000 条）
// 3. 统计 TPS 和错误率
func TestPerformance_HighThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过压力测试（使用 -short 标志）")
	}

	concurrentUsers := 100
	messagesPerUser := 50

	var wg sync.WaitGroup
	var mu sync.Mutex
	successCount := 0
	errorCount := 0

	t.Logf("开始高吞吐量测试: %d 用户 x %d 消息 = %d 总消息",
		concurrentUsers, messagesPerUser, concurrentUsers*messagesPerUser)

	start := time.Now()

	for i := 0; i < concurrentUsers; i++ {
		wg.Add(1)
		go func(userIdx int) {
			defer wg.Done()

			// 创建用户和连接
			userA := createTestUser()
			userB := createTestUser()

			ws, err := connectWebSocket(userA.Token)
			if err != nil {
				mu.Lock()
				errorCount += messagesPerUser
				mu.Unlock()
				return
			}
			defer ws.Close()

			// 建立会话
			wsSend(ws, "message", map[string]interface{}{
				"receiver_id":  userB.ID.String(),
				"message_type": "text",
				"content":      "Init",
			})
			msg, _ := wsReceive(ws, 3*time.Second)
			if msg == nil {
				mu.Lock()
				errorCount += messagesPerUser
				mu.Unlock()
				return
			}

			convID := msg["data"].(map[string]interface{})["conversation_id"].(string)

			// 快速发送消息
			for j := 0; j < messagesPerUser; j++ {
				err := wsSend(ws, "message", map[string]interface{}{
					"conversation_id": convID,
					"message_type":    "text",
					"content":         fmt.Sprintf("Msg %d from user %d", j, userIdx),
				})

				if err == nil {
					_, err = wsReceive(ws, 3*time.Second)
				}

				mu.Lock()
				if err == nil {
					successCount++
				} else {
					errorCount++
				}
				mu.Unlock()
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	totalMessages := concurrentUsers * messagesPerUser
	tps := float64(successCount) / duration.Seconds()
	successRate := (successCount * 100) / totalMessages

	t.Log("========================================")
	t.Log("高吞吐量测试报告")
	t.Log("========================================")
	t.Logf("总消息数: %d", totalMessages)
	t.Logf("成功: %d (%.1f%%)", successCount, float64(successRate))
	t.Logf("失败: %d", errorCount)
	t.Logf("总耗时: %v", duration)
	t.Logf("TPS: %.2f 消息/秒", tps)
	t.Log("========================================")

	assert.GreaterOrEqual(t, successRate, 70, "成功率应 >= 70%")
	assert.GreaterOrEqual(t, tps, 50.0, "TPS 应 >= 50 消息/秒")
}

package test

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ============================================
// 真实场景压测 - 可配置用户数，模拟真实交互
// ============================================
//
// 使用方法：
//   go test -v -run TestRealisticLoad ./test/ -timeout 10m
//
// 配置参数：
//   直接修改 TestRealisticLoad_10KUsers 函数开头的配置常量
//   包括：totalUsers, onlineDuration, thinkTimeMin/Max,
//         msgCountMin/Max, validationSampleRate 等
// ============================================

// SystemMetrics 系统资源指标
type SystemMetrics struct {
	Timestamp      time.Time
	MemoryUsageMB  float64
	GoroutineCount int
	HeapAllocMB    float64
	HeapSysMB      float64
	NumGC          uint32
	CPUCount       int
}

// MessageValidation 消息验证结果
type MessageValidation struct {
	ConversationID     string
	SenderID           string
	ReceiverID         string
	MessageID          string
	ReceiverWasOnline  bool // 验证时接收方是否在线
	MessageSent        bool // 发送方收到确认
	ReceiverGotMessage bool // 接收方 WebSocket 收到消息推送
	ReceiverGotConvUpd bool // 接收方 WebSocket 收到会话更新
	InSenderConvList   bool // 在发送方会话列表中
	InReceiverConvList bool // 在接收方会话列表中
	InSenderHistory    bool // 在发送方消息历史中
	InReceiverHistory  bool // 在接收方消息历史中
	ReceiverUnreadGt0  bool // 接收方未读计数 > 0
	LatencyMs          int64
	Error              string
}

// UserContext 用户上下文（用于跟踪接收的消息）
type UserContext struct {
	ID                   string
	Token                string
	IsOnline             bool            // 当前是否在线
	ReceivedMessages     map[string]bool // messageID -> received
	ReceivedConvUpd      map[string]bool // conversationID -> received update
	ReceivedOnlineStatus map[string]bool // userID -> received online status update
	ReceivedOfflineMsg   map[string]bool // messageID -> received offline message
	ReceivedTyping       map[string]bool // conversationID -> received typing indicator
	ReceivedRead         map[string]bool // messageID -> received read receipt
	ReceivedRecalled     map[string]bool // messageID -> received recall notification
	ReceivedUnreadUpd    map[string]int  // conversationID -> last unread count received
	FirstMsgBlocked      map[string]bool // receiverID -> first message to this user was blocked
	SentHeartbeats       int             // 发送的心跳数
	mu                   sync.RWMutex    // 保护上述字段的读写
	wsMutex              sync.Mutex      // 保护 WebSocket 写操作（防止并发写）
}

// collectSystemMetrics 采集系统资源指标
func collectSystemMetrics() SystemMetrics {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return SystemMetrics{
		Timestamp:      time.Now(),
		MemoryUsageMB:  float64(m.Alloc) / 1024 / 1024,
		GoroutineCount: runtime.NumGoroutine(),
		HeapAllocMB:    float64(m.HeapAlloc) / 1024 / 1024,
		HeapSysMB:      float64(m.HeapSys) / 1024 / 1024,
		NumGC:          m.NumGC,
		CPUCount:       runtime.NumCPU(),
	}
}

// TestRealisticLoad_10KUsers 模拟真实用户聊天场景 - 专业版
//
// 测试目标：
// - 可配置用户数（默认 10000，环境变量 LOAD_TEST_USERS）
// - 逐渐上线所有用户（默认 60 秒，环境变量 LOAD_TEST_RAMP_UP）
// - 每个用户在线时长可配置（默认 20 秒，环境变量 LOAD_TEST_ONLINE_TIME）
// - 监控系统资源（内存、CPU、Goroutine）
// - 监控网络流量（发送/接收字节数）
// - 全链路数据验证（WebSocket 推送 + HTTP API 查询）
//
// 真实场景模拟：
// 1. 用户逐渐上线（而非同时上线）
// 2. 用户有思考时间（可配置，默认 800-2000ms）
// 3. 用户保持连接（接收消息、处理推送）
// 4. 70% 用户会主动发消息，30% 只接收
// 5. 验证双向体验：发送方 + 接收方（WebSocket推送 + HTTP查询）
func TestRealisticLoad_10KUsers(t *testing.T) {

	// ========================================
	// 📝 测试配置（直接在这里修改参数）
	// ========================================

	// 服务地址配置
	BaseURL = "http://localhost:8083" // HTTP API 地址
	WSURL = "ws://localhost:8083"     // WebSocket 地址

	// 测试规模配置
	totalUsers := 2000                 // 总用户数
	onlineDuration := 30 * time.Second // 单用户在线时长
	rampUpDuration := 60 * time.Second // 用户上线时间（逐渐上线）
	thinkTimeMin := 800                // 思考时间最小值（毫秒）
	thinkTimeMax := 2000               // 思考时间最大值（毫秒）
	msgCountMin := 2                   // 每人最少发送消息数
	msgCountMax := 20                  // 每人最多发送消息数
	validationSampleRate := 10         // 验证采样率（百分比，1-100）
	// ========================================

	// 统计指标
	var (
		totalConnections      int64
		successConnections    int64
		failedConnections     int64
		totalMessagesSent     int64
		successMessages       int64
		failedMessages        int64
		firstMsgLimitBlocked  int64 // 首条消息限制导致的失败
		totalMessagesRecv     int64
		totalReconnections    int64 // 重新上线次数
		totalOfflineMsgRecv   int64 // 收到的离线消息数
		totalOnlineStatusRecv int64 // 收到的在线状态推送数
		activeUsers           int64
		peakActiveUsers       int64
		totalBytesSent        int64
		totalBytesRecv        int64
	)

	// 延迟数据
	var latencies []time.Duration
	var latenciesMu sync.Mutex

	// 验证数据
	var validations []*MessageValidation
	var validationsMu sync.Mutex

	// 系统指标采集
	var systemMetrics []SystemMetrics
	var metricsMu sync.Mutex

	// 用户上下文映射（用于跟踪接收方收到的消息）
	userContexts := make(map[string]*UserContext)
	var userContextsMu sync.RWMutex

	t.Log("========================================")
	t.Log("🚀 真实场景压测开始")
	t.Log("========================================")
	t.Logf("目标用户数: %d", totalUsers)
	t.Logf("上线时间: %v", rampUpDuration)
	t.Logf("单用户在线时长: %v", onlineDuration)
	t.Logf("思考时间: %d-%d ms", thinkTimeMin, thinkTimeMax)
	t.Logf("消息数量: %d-%d 条/人", msgCountMin, msgCountMax)
	t.Logf("Gateway URL: %s", BaseURL)
	t.Logf("CPU 核心数: %d", runtime.NumCPU())
	t.Log("========================================")

	startTime := time.Now()
	var wg sync.WaitGroup

	userInterval := rampUpDuration / time.Duration(totalUsers)

	// 用户池
	userPool := make([]*UserContext, 0, totalUsers)
	var userPoolMu sync.RWMutex

	// 采集系统指标 goroutine
	stopMetrics := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				metrics := collectSystemMetrics()
				metricsMu.Lock()
				systemMetrics = append(systemMetrics, metrics)
				metricsMu.Unlock()
			case <-stopMetrics:
				return
			}
		}
	}()

	// 进度报告 goroutine
	stopProgress := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				elapsed := time.Since(startTime)
				active := atomic.LoadInt64(&activeUsers)
				totalConn := atomic.LoadInt64(&totalConnections)
				successConn := atomic.LoadInt64(&successConnections)
				sentMsg := atomic.LoadInt64(&totalMessagesSent)
				successMsg := atomic.LoadInt64(&successMessages)
				blockedMsg := atomic.LoadInt64(&firstMsgLimitBlocked)
				bytesSent := atomic.LoadInt64(&totalBytesSent)
				bytesRecv := atomic.LoadInt64(&totalBytesRecv)

				metrics := collectSystemMetrics()

				t.Logf("[%v] 进度报告:", elapsed.Round(time.Second))
				t.Logf("  连接: %d/%d (成功率 %.1f%%)", totalConn, totalUsers,
					float64(successConn)*100/float64(max(totalConn, 1)))
				t.Logf("  活跃: %d 用户", active)
				t.Logf("  消息: %d 发送, %d 成功 (%.1f%%), %d 首条限制",
					sentMsg, successMsg, float64(successMsg)*100/float64(max(sentMsg, 1)), blockedMsg)
				t.Logf("  流量: 发送 %.2f MB, 接收 %.2f MB",
					float64(bytesSent)/1024/1024, float64(bytesRecv)/1024/1024)
				t.Logf("  内存: %.2f MB (堆 %.2f MB)", metrics.MemoryUsageMB, metrics.HeapAllocMB)
				t.Logf("  协程: %d, GC 次数: %d", metrics.GoroutineCount, metrics.NumGC)
			case <-stopProgress:
				return
			}
		}
	}()

	// 逐渐上线用户
	for i := 0; i < totalUsers; i++ {
		wg.Add(1)

		go func(userIdx int) {
			defer wg.Done()

			atomic.AddInt64(&totalConnections, 1)

			// 1. 创建用户
			user := createTestUser()
			userCtx := &UserContext{
				ID:                   user.ID.String(),
				Token:                user.Token,
				IsOnline:             false, // 初始离线，连接成功后设为 true
				ReceivedMessages:     make(map[string]bool),
				ReceivedConvUpd:      make(map[string]bool),
				ReceivedOnlineStatus: make(map[string]bool),
				ReceivedOfflineMsg:   make(map[string]bool),
				ReceivedTyping:       make(map[string]bool),
				ReceivedRead:         make(map[string]bool),
				ReceivedRecalled:     make(map[string]bool),
				ReceivedUnreadUpd:    make(map[string]int),
				FirstMsgBlocked:      make(map[string]bool),
				SentHeartbeats:       0,
			}

			// 注册到用户上下文映射
			userContextsMu.Lock()
			userContexts[userCtx.ID] = userCtx
			userContextsMu.Unlock()

			userPoolMu.Lock()
			userPool = append(userPool, userCtx)
			userPoolMu.Unlock()

			// 2. 建立 WebSocket 连接
			ws, err := connectWebSocket(user.Token)
			if err != nil {
				atomic.AddInt64(&failedConnections, 1)
				log.Printf("❌ [Connection Failed] User %d (%s) failed to connect: %v", userIdx, userCtx.ID, err)
				return
			}
			defer ws.Close()

			atomic.AddInt64(&successConnections, 1)
			atomic.AddInt64(&activeUsers, 1)
			defer atomic.AddInt64(&activeUsers, -1)

			// 标记用户上线
			userCtx.mu.Lock()
			userCtx.IsOnline = true
			userCtx.mu.Unlock()
			defer func() {
				// 标记用户下线
				userCtx.mu.Lock()
				userCtx.IsOnline = false
				userCtx.mu.Unlock()
			}()

			// 更新峰值
			for {
				current := atomic.LoadInt64(&activeUsers)
				peak := atomic.LoadInt64(&peakActiveUsers)
				if current <= peak || atomic.CompareAndSwapInt64(&peakActiveUsers, peak, current) {
					break
				}
			}

			// 3. 启动消息接收 goroutine（使用wsReceiveRaw接收所有消息）
			// confirmChan用于传递发送确认消息（避免主goroutine和接收goroutine竞争读取WebSocket）
			confirmChan := make(chan map[string]interface{}, 10)
			recvDone := make(chan struct{})
			go func() {
				defer close(recvDone)
				defer close(confirmChan)
				for {
					// 使用 wsReceiveRaw 接收所有消息（包括系统推送）
					msg, err := wsReceiveRaw(ws, 20*time.Second)
					if err != nil {
						// WebSocket 连接断开，立即标记用户离线（提高测试精度）
						userCtx.mu.Lock()
						userCtx.IsOnline = false
						userCtx.mu.Unlock()
						return
					}
					atomic.AddInt64(&totalMessagesRecv, 1)

					// 估算接收字节数（JSON 编码后）
					if msgBytes, err := json.Marshal(msg); err == nil {
						atomic.AddInt64(&totalBytesRecv, int64(len(msgBytes)))
					}

					// 处理不同类型的消息
					msgType, _ := msg["type"].(string)
					data, _ := msg["data"].(map[string]interface{})

					switch msgType {
					case "message":
						// 接收到新消息（可能是发送确认，也可能是收到别人的消息）
						if data != nil {
							if msgID, ok := data["id"].(string); ok {
								userCtx.mu.Lock()
								userCtx.ReceivedMessages[msgID] = true
								userCtx.mu.Unlock()
							}
							// 如果有sender_id且是自己发的，说明是发送确认，通过channel传递
							if senderID, ok := data["sender_id"].(string); ok && senderID == userCtx.ID {
								select {
								case confirmChan <- msg:
								default:
									// channel满了，丢弃（不应该发生）
								}
							}
						}

					case "conversation_update":
						// 接收到会话更新
						if data != nil {
							if convID, ok := data["conversation_id"].(string); ok {
								userCtx.mu.Lock()
								userCtx.ReceivedConvUpd[convID] = true
								userCtx.mu.Unlock()
							}
						}

					case "offline_message":
						// 接收到离线消息（首次连接通常不会有，但为了代码完整性保留）
						if data != nil {
							if msgID, ok := data["id"].(string); ok {
								userCtx.mu.Lock()
								userCtx.ReceivedMessages[msgID] = true
								userCtx.ReceivedOfflineMsg[msgID] = true
								userCtx.mu.Unlock()
								atomic.AddInt64(&totalOfflineMsgRecv, 1)
							}
						}

					case "online_status_update":
						// 接收到在线状态更新
						if data != nil {
							if targetUserID, ok := data["user_id"].(string); ok {
								userCtx.mu.Lock()
								userCtx.ReceivedOnlineStatus[targetUserID] = true
								userCtx.mu.Unlock()
							}
						}

					case "error":
						// 接收到错误消息（可能是首条消息限制）
						// 也通过confirmChan传递给发送方
						select {
						case confirmChan <- msg:
						default:
						}

					case "typing":
						// 接收到正在输入提示
						if data != nil {
							if convID, ok := data["conversation_id"].(string); ok {
								userCtx.mu.Lock()
								userCtx.ReceivedTyping[convID] = true
								userCtx.mu.Unlock()
							}
						}

					case "read":
						// 接收到已读回执
						if data != nil {
							if msgID, ok := data["message_id"].(string); ok {
								userCtx.mu.Lock()
								userCtx.ReceivedRead[msgID] = true
								userCtx.mu.Unlock()
							}
						}

					case "recalled":
						// 接收到消息撤回通知
						if data != nil {
							if msgID, ok := data["message_id"].(string); ok {
								userCtx.mu.Lock()
								userCtx.ReceivedRecalled[msgID] = true
								userCtx.mu.Unlock()
							}
						}

					case "unread_count_update":
						// 接收到未读数更新
						if data != nil {
							if convID, ok := data["conversation_id"].(string); ok {
								if unreadCount, ok := data["unread_count"].(float64); ok {
									userCtx.mu.Lock()
									userCtx.ReceivedUnreadUpd[convID] = int(unreadCount)
									userCtx.mu.Unlock()
								}
							}
						}
					}
				}
			}()

			// 3.5 启动心跳goroutine（每15秒发送一次心跳，刷新在线状态）
			heartbeatDone := make(chan struct{})
			go func() {
				defer close(heartbeatDone)
				ticker := time.NewTicker(15 * time.Second)
				defer ticker.Stop()

				for {
					select {
					case <-ticker.C:
						userCtx.wsMutex.Lock()
						err := wsSend(ws, "heartbeat", map[string]interface{}{})
						userCtx.wsMutex.Unlock()
						if err != nil {
							return
						}
						userCtx.mu.Lock()
						userCtx.SentHeartbeats++
						userCtx.mu.Unlock()
					case <-recvDone:
						return
					}
				}
			}()

			// 4. 模拟用户行为
			endTime := time.Now().Add(onlineDuration)
			isActiveSender := rand.Intn(100) < 70

			// 用于追踪已发送消息的接收方（避免首条消息限制）
			sentToUsers := make(map[string]bool)

			if isActiveSender {
				numMessages := rand.Intn(msgCountMax-msgCountMin+1) + msgCountMin

				for msgCount := 0; msgCount < numMessages && time.Now().Before(endTime); msgCount++ {
					// 思考时间
					thinkTime := time.Duration(rand.Intn(thinkTimeMax-thinkTimeMin)+thinkTimeMin) * time.Millisecond
					time.Sleep(thinkTime)

					if time.Now().After(endTime) {
						break
					}

					// 选择聊天对象
					userPoolMu.RLock()
					poolSize := len(userPool)
					userPoolMu.RUnlock()

					if poolSize < 10 {
						continue
					}

					// 优先选择没发过消息的用户（避免首条消息限制）
					var target *UserContext
					maxAttempts := 10
					for attempt := 0; attempt < maxAttempts; attempt++ {
						userPoolMu.RLock()
						targetIdx := rand.Intn(poolSize)
						candidate := userPool[targetIdx]
						userPoolMu.RUnlock()

						if candidate.ID == userCtx.ID {
							continue // 不能给自己发
						}

						// 如果没发过消息给这个人，优先选择
						if !sentToUsers[candidate.ID] {
							target = candidate
							break
						}

						// 如果尝试多次都是发过消息的，那就用最后一个
						if attempt == maxAttempts-1 {
							target = candidate
						}
					}

					if target == nil || target.ID == userCtx.ID {
						continue
					}

					// 发送消息并验证
					validation := &MessageValidation{
						SenderID:   userCtx.ID,
						ReceiverID: target.ID,
					}

					atomic.AddInt64(&totalMessagesSent, 1)
					msgStart := time.Now()

					// 在发送消息之前，记录接收方当前是否在线（避免时序问题）
					target.mu.RLock()
					receiverOnlineAtSend := target.IsOnline
					target.mu.RUnlock()

					messageContent := fmt.Sprintf("Hello from user %d at %v. This is a test message with more content to simulate real-world usage patterns. The quick brown fox jumps over the lazy dog. Testing message delivery system with WebSocket and database persistence.", userIdx, time.Now().Format("15:04:05"))
					userCtx.wsMutex.Lock()
					err := wsSend(ws, "message", map[string]interface{}{
						"receiver_id":  target.ID,
						"message_type": "text",
						"content":      messageContent,
					})
					userCtx.wsMutex.Unlock()

					// 估算发送字节数
					if err == nil {
						estimatedBytes := len(messageContent) + 200 // JSON overhead
						atomic.AddInt64(&totalBytesSent, int64(estimatedBytes))
					}

					if err != nil {
						atomic.AddInt64(&failedMessages, 1)
						validation.Error = fmt.Sprintf("发送失败: %v", err)
						validationsMu.Lock()
						validations = append(validations, validation)
						validationsMu.Unlock()
						continue
					}

					// 等待发送确认（从confirmChan读取，避免与接收goroutine竞争）
					var confirmMsg map[string]interface{}
					select {
					case confirmMsg = <-confirmChan:
						// 收到确认消息
					case <-time.After(3 * time.Second):
						// 超时
						atomic.AddInt64(&failedMessages, 1)
						validation.Error = "未收到确认: 超时"
						validationsMu.Lock()
						validations = append(validations, validation)
						validationsMu.Unlock()
						continue
					}

					latency := time.Since(msgStart)

					// 检查是否是error消息（首条消息限制）
					if msgType, ok := confirmMsg["type"].(string); ok && msgType == "error" {
						atomic.AddInt64(&failedMessages, 1)
						atomic.AddInt64(&firstMsgLimitBlocked, 1)
						validation.Error = "首条消息限制"

						// 从错误消息中提取详细信息
						if data, ok := confirmMsg["data"].(map[string]interface{}); ok {
							if errMsg, ok := data["message"].(string); ok {
								validation.Error = fmt.Sprintf("首条消息限制: %s", errMsg)
							}
						}

						// 记录对这个接收方的首条消息被拦截
						userCtx.mu.Lock()
						userCtx.FirstMsgBlocked[target.ID] = true
						userCtx.mu.Unlock()

						validationsMu.Lock()
						validations = append(validations, validation)
						validationsMu.Unlock()
						continue
					}

					atomic.AddInt64(&successMessages, 1)
					validation.MessageSent = true
					validation.LatencyMs = latency.Milliseconds()

					// 标记已发送给这个用户
					sentToUsers[target.ID] = true

					// 提取消息详情
					if data, ok := confirmMsg["data"].(map[string]interface{}); ok {
						if msgID, ok := data["id"].(string); ok {
							validation.MessageID = msgID
						}
						if convID, ok := data["conversation_id"].(string); ok {
							validation.ConversationID = convID
						}
					}

					latenciesMu.Lock()
					latencies = append(latencies, latency)
					latenciesMu.Unlock()

					// 全链路验证（可配置采样率，避免过度请求）
					if rand.Intn(100) < validationSampleRate && validation.ConversationID != "" && validation.MessageID != "" {
						// 复制验证对象，避免闭包问题
						v := &MessageValidation{
							ConversationID: validation.ConversationID,
							SenderID:       validation.SenderID,
							ReceiverID:     validation.ReceiverID,
							MessageID:      validation.MessageID,
							MessageSent:    validation.MessageSent,
							LatencyMs:      validation.LatencyMs,
						}

						// 传递接收方的在线状态（发送消息时的快照）和token
						go func(targetCtx *UserContext, senderCtx *UserContext, senderToken string, wasOnlineAtSend bool) {
							// 等待数据库写入和WebSocket推送（2秒更保险）
							time.Sleep(2 * time.Second)

							// === 检查是否是首条消息场景 ===
							senderCtx.mu.RLock()
							isFirstMsgBlocked := senderCtx.FirstMsgBlocked[v.ReceiverID]
							senderCtx.mu.RUnlock()

							// === 检查接收方在验证时是否仍然在线 ===
							targetCtx.mu.RLock()
							isStillOnlineNow := targetCtx.IsOnline
							targetCtx.mu.RUnlock()

							// === 如果发送时在线但验证时已离线，按离线场景验证 ===
							// 这是正常情况：用户在消息发送过程中离线了（30秒在线时长结束）
							// 此时不应该验证 WebSocket 推送，而应该只验证数据库持久化
							isReceiverOnline := wasOnlineAtSend && isStillOnlineNow

							// 保存接收方在线状态到验证对象
							v.ReceiverWasOnline = isReceiverOnline

							// === 验证 WebSocket 推送 ===

							// 1. 验证接收方是否收到消息推送（仅在线用户需要验证）
							targetCtx.mu.RLock()
							v.ReceiverGotMessage = targetCtx.ReceivedMessages[v.MessageID]
							targetCtx.mu.RUnlock()

							// 2. 验证接收方是否收到会话更新推送（仅在线用户需要验证）
							targetCtx.mu.RLock()
							v.ReceiverGotConvUpd = targetCtx.ReceivedConvUpd[v.ConversationID]
							targetCtx.mu.RUnlock()

							// === 验证 HTTP API ===

							// 3. 检查发送方的会话列表（使用传入的senderToken）
							v.InSenderConvList = verifyInConversationList(senderToken, v.ConversationID)

							// 4. 检查发送方的消息历史
							v.InSenderHistory = verifyInMessageHistory(senderToken, v.ConversationID, v.MessageID)

							// 5. 检查接收方的会话列表
							v.InReceiverConvList = verifyInConversationList(targetCtx.Token, v.ConversationID)

							// 6. 检查接收方的消息历史
							v.InReceiverHistory = verifyInMessageHistory(targetCtx.Token, v.ConversationID, v.MessageID)

							// 7. 检查接收方是否收到未读数更新推送（通过WebSocket验证，避免HTTP查询时序问题）
							targetCtx.mu.RLock()
							receivedUnreadCount, gotUnreadUpdate := targetCtx.ReceivedUnreadUpd[v.ConversationID]
							targetCtx.mu.RUnlock()
							v.ReceiverUnreadGt0 = gotUnreadUpdate && receivedUnreadCount > 0

							// 汇总错误
							if !v.InSenderConvList || !v.InSenderHistory {
								v.Error = fmt.Sprintf("发送方验证失败: 会话列表=%v, 消息历史=%v",
									v.InSenderConvList, v.InSenderHistory)
							} else if isReceiverOnline && (!v.ReceiverGotMessage || (!v.ReceiverGotConvUpd && !isFirstMsgBlocked)) {
								// 仅在接收方在线时才验证 WebSocket 推送
								// 如果是首条消息被拦截的场景，不验证会话更新推送（因为会话都没创建）
								v.Error = fmt.Sprintf("接收方WS推送失败(在线): 消息=%v, 会话更新=%v",
									v.ReceiverGotMessage, v.ReceiverGotConvUpd)
							} else if !v.InReceiverConvList || !v.InReceiverHistory {
								v.Error = fmt.Sprintf("接收方验证失败: 会话列表=%v, 消息历史=%v",
									v.InReceiverConvList, v.InReceiverHistory)
							} else if isReceiverOnline && !v.ReceiverUnreadGt0 {
								// 仅在接收方在线时才验证未读数推送
								v.Error = fmt.Sprintf("接收方未读数推送验证失败(在线): got_update=%v, count=%d",
									gotUnreadUpdate, receivedUnreadCount)
							}

							validationsMu.Lock()
							validations = append(validations, v)
							validationsMu.Unlock()
						}(target, userCtx, userCtx.Token, receiverOnlineAtSend)
					}
				}
			}

			// 等待剩余在线时间
			remainingTime := endTime.Sub(time.Now())
			if remainingTime > 0 {
				time.Sleep(remainingTime)
			}

			// 关闭接收和心跳goroutine
			select {
			case <-recvDone:
			case <-time.After(1 * time.Second):
			}
			select {
			case <-heartbeatDone:
			case <-time.After(1 * time.Second):
			}

			// 20%的用户会在下线后重新上线（测试离线消息、在线状态推送、重连后继续发送消息）
			shouldReconnect := rand.Intn(100) < 20
			if shouldReconnect {
				atomic.AddInt64(&totalReconnections, 1)

				// 关闭当前连接（触发下线）
				ws.Close()

				// 立即标记用户离线（提高测试精度）
				userCtx.mu.Lock()
				userCtx.IsOnline = false
				userCtx.mu.Unlock()

				// 等待2秒（模拟下线时间，期间可能收到离线消息）
				time.Sleep(2 * time.Second)

				// 重新上线
				ws2, err := connectWebSocket(user.Token)
				if err != nil {
					// 重连失败，不需要加回 activeUsers（因为已经下线了）
					log.Printf("❌ [Reconnection Failed] User %d (%s) failed to reconnect: %v", userIdx, userCtx.ID, err)
					return
				}
				defer ws2.Close()
				defer atomic.AddInt64(&activeUsers, -1) // 保证第二次连接结束时减1

				atomic.AddInt64(&activeUsers, 1)

				// 标记用户重新上线
				userCtx.mu.Lock()
				userCtx.IsOnline = true
				userCtx.mu.Unlock()

				// 启动新的接收goroutine（完整的消息处理逻辑，和首次连接一致）
				confirmChan2 := make(chan map[string]interface{}, 10)
				recvDone2 := make(chan struct{})

				go func() {
					defer close(recvDone2)
					defer close(confirmChan2)
					for {
						msg, err := wsReceiveRaw(ws2, 20*time.Second)
						if err != nil {
							// WebSocket 连接断开，立即标记用户离线（提高测试精度）
							userCtx.mu.Lock()
							userCtx.IsOnline = false
							userCtx.mu.Unlock()
							return
						}
						atomic.AddInt64(&totalMessagesRecv, 1)

						// 估算接收字节数
						if msgBytes, err := json.Marshal(msg); err == nil {
							atomic.AddInt64(&totalBytesRecv, int64(len(msgBytes)))
						}

						msgType, _ := msg["type"].(string)
						data, _ := msg["data"].(map[string]interface{})

						switch msgType {
						case "message":
							if data != nil {
								if msgID, ok := data["id"].(string); ok {
									userCtx.mu.Lock()
									userCtx.ReceivedMessages[msgID] = true
									userCtx.mu.Unlock()
								}
								if senderID, ok := data["sender_id"].(string); ok && senderID == userCtx.ID {
									select {
									case confirmChan2 <- msg:
									default:
									}
								}
							}

						case "conversation_update":
							if data != nil {
								if convID, ok := data["conversation_id"].(string); ok {
									userCtx.mu.Lock()
									userCtx.ReceivedConvUpd[convID] = true
									userCtx.mu.Unlock()
								}
							}

						case "offline_message":
							// 重连后收到的离线消息
							if data != nil {
								if msgID, ok := data["id"].(string); ok {
									userCtx.mu.Lock()
									userCtx.ReceivedMessages[msgID] = true
									userCtx.ReceivedOfflineMsg[msgID] = true
									userCtx.mu.Unlock()
									atomic.AddInt64(&totalOfflineMsgRecv, 1)
								}
							}

						case "online_status_update":
							if data != nil {
								if targetUserID, ok := data["user_id"].(string); ok {
									userCtx.mu.Lock()
									userCtx.ReceivedOnlineStatus[targetUserID] = true
									userCtx.mu.Unlock()
									atomic.AddInt64(&totalOnlineStatusRecv, 1)
								}
							}

						case "error":
							select {
							case confirmChan2 <- msg:
							default:
							}

						case "typing":
							if data != nil {
								if convID, ok := data["conversation_id"].(string); ok {
									userCtx.mu.Lock()
									userCtx.ReceivedTyping[convID] = true
									userCtx.mu.Unlock()
								}
							}

						case "read":
							if data != nil {
								if msgID, ok := data["message_id"].(string); ok {
									userCtx.mu.Lock()
									userCtx.ReceivedRead[msgID] = true
									userCtx.mu.Unlock()
								}
							}

						case "recalled":
							if data != nil {
								if msgID, ok := data["message_id"].(string); ok {
									userCtx.mu.Lock()
									userCtx.ReceivedRecalled[msgID] = true
									userCtx.mu.Unlock()
								}
							}

						case "unread_count_update":
							if data != nil {
								if convID, ok := data["conversation_id"].(string); ok {
									if unreadCount, ok := data["unread_count"].(float64); ok {
										userCtx.mu.Lock()
										userCtx.ReceivedUnreadUpd[convID] = int(unreadCount)
										userCtx.mu.Unlock()
									}
								}
							}
						}
					}
				}()

				// 重连后也发送1-2条消息（30%概率发送）
				reconnectEndTime := time.Now().Add(onlineDuration / 2) // 重连后在线时间为首次的一半
				if rand.Intn(100) < 30 {
					numReconnectMsg := rand.Intn(2) + 1 // 1-2条消息

					for msgCount := 0; msgCount < numReconnectMsg && time.Now().Before(reconnectEndTime); msgCount++ {
						time.Sleep(time.Duration(rand.Intn(2000)+1000) * time.Millisecond) // 1-3秒思考时间

						if time.Now().After(reconnectEndTime) {
							break
						}

						// 随机选择聊天对象
						userPoolMu.RLock()
						poolSize := len(userPool)
						userPoolMu.RUnlock()

						if poolSize < 10 {
							continue
						}

						targetIdx := rand.Intn(poolSize)
						userPoolMu.RLock()
						target := userPool[targetIdx]
						userPoolMu.RUnlock()

						if target.ID == userCtx.ID {
							continue
						}

						// 发送消息
						atomic.AddInt64(&totalMessagesSent, 1)
						messageContent := fmt.Sprintf("Reconnected message from user %d at %v. This is a test message with more content to simulate real-world usage patterns. The quick brown fox jumps over the lazy dog. Testing reconnection and message delivery after going offline.", userIdx, time.Now().Format("15:04:05"))
						userCtx.wsMutex.Lock()
						err := wsSend(ws2, "message", map[string]interface{}{
							"receiver_id":  target.ID,
							"message_type": "text",
							"content":      messageContent,
						})
						userCtx.wsMutex.Unlock()

						if err == nil {
							estimatedBytes := len(messageContent) + 200
							atomic.AddInt64(&totalBytesSent, int64(estimatedBytes))
						}

						if err != nil {
							atomic.AddInt64(&failedMessages, 1)
							continue
						}

						// 等待确认
						select {
						case confirmMsg := <-confirmChan2:
							if msgType, ok := confirmMsg["type"].(string); ok && msgType == "error" {
								atomic.AddInt64(&failedMessages, 1)
								atomic.AddInt64(&firstMsgLimitBlocked, 1)
							} else {
								atomic.AddInt64(&successMessages, 1)
							}
						case <-time.After(3 * time.Second):
							atomic.AddInt64(&failedMessages, 1)
						}
					}
				}

				// 等待剩余在线时间
				remainingTime := reconnectEndTime.Sub(time.Now())
				if remainingTime > 0 {
					time.Sleep(remainingTime)
				}

				// 关闭接收goroutine
				select {
				case <-recvDone2:
				case <-time.After(1 * time.Second):
				}

				// 第二次连接的关闭和 activeUsers 减1 已经在上面的 defer 中处理
			}

		}(i)

		time.Sleep(userInterval)
	}

	// 等待所有用户完成
	t.Log("⏳ 等待所有用户完成...")
	wg.Wait()

	// 等待验证goroutine完成（额外等待3秒）
	t.Log("⏳ 等待验证完成...")
	time.Sleep(3 * time.Second)

	close(stopProgress)
	close(stopMetrics)

	totalDuration := time.Since(startTime)

	// 统计结果
	totalConn := atomic.LoadInt64(&totalConnections)
	successConn := atomic.LoadInt64(&successConnections)
	failedConn := atomic.LoadInt64(&failedConnections)
	sentMsg := atomic.LoadInt64(&totalMessagesSent)
	successMsg := atomic.LoadInt64(&successMessages)
	failedMsg := atomic.LoadInt64(&failedMessages)
	blockedMsg := atomic.LoadInt64(&firstMsgLimitBlocked)
	recvMsg := atomic.LoadInt64(&totalMessagesRecv)
	reconnections := atomic.LoadInt64(&totalReconnections)
	offlineMsgRecv := atomic.LoadInt64(&totalOfflineMsgRecv)
	onlineStatusRecv := atomic.LoadInt64(&totalOnlineStatusRecv)
	peak := atomic.LoadInt64(&peakActiveUsers)
	bytesSent := atomic.LoadInt64(&totalBytesSent)
	bytesRecv := atomic.LoadInt64(&totalBytesRecv)

	connSuccessRate := float64(successConn) * 100 / float64(totalConn)
	msgSuccessRate := float64(0)
	if sentMsg > 0 {
		msgSuccessRate = float64(successMsg) * 100 / float64(sentMsg)
	}

	qps := float64(successMsg) / totalDuration.Seconds()
	bandwidth := (float64(bytesSent) + float64(bytesRecv)) / totalDuration.Seconds() / 1024 / 1024 // MB/s

	// 计算延迟统计
	var avgLatency, p50Latency, p95Latency, p99Latency, maxLatency time.Duration
	if len(latencies) > 0 {
		// 排序延迟数据（使用标准库的 O(n log n) 算法）
		sort.Slice(latencies, func(i, j int) bool {
			return latencies[i] < latencies[j]
		})

		var total time.Duration
		for _, l := range latencies {
			total += l
		}
		avgLatency = total / time.Duration(len(latencies))
		p50Latency = latencies[len(latencies)*50/100]
		p95Latency = latencies[len(latencies)*95/100]
		p99Latency = latencies[len(latencies)*99/100]
		maxLatency = latencies[len(latencies)-1]
	}

	// 统计验证结果
	totalValidations := len(validations)
	var (
		// 总体统计
		fullChainValid   int
		validationErrors []string

		// 在线用户统计
		onlineCount               int
		onlineSentValid           int
		onlineRecvMsgValid        int
		onlineRecvConvUpdValid    int
		onlineSenderConvValid     int
		onlineSenderHistValid     int
		onlineReceiverConvValid   int
		onlineReceiverHistValid   int
		onlineReceiverUnreadValid int

		// 离线用户统计
		offlineCount             int
		offlineSentValid         int
		offlineSenderConvValid   int
		offlineSenderHistValid   int
		offlineReceiverConvValid int
		offlineReceiverHistValid int
	)

	for _, v := range validations {
		if v.ReceiverWasOnline {
			// 统计在线用户
			onlineCount++
			if v.MessageSent {
				onlineSentValid++
			}
			if v.ReceiverGotMessage {
				onlineRecvMsgValid++
			}
			if v.ReceiverGotConvUpd {
				onlineRecvConvUpdValid++
			}
			if v.InSenderConvList {
				onlineSenderConvValid++
			}
			if v.InSenderHistory {
				onlineSenderHistValid++
			}
			if v.InReceiverConvList {
				onlineReceiverConvValid++
			}
			if v.InReceiverHistory {
				onlineReceiverHistValid++
			}
			if v.ReceiverUnreadGt0 {
				onlineReceiverUnreadValid++
			}

			// 在线用户全链路验证
			if v.MessageSent && v.ReceiverGotMessage && v.ReceiverGotConvUpd &&
				v.InSenderConvList && v.InSenderHistory &&
				v.InReceiverConvList && v.InReceiverHistory &&
				v.ReceiverUnreadGt0 {
				fullChainValid++
			}
		} else {
			// 统计离线用户
			offlineCount++
			if v.MessageSent {
				offlineSentValid++
			}
			if v.InSenderConvList {
				offlineSenderConvValid++
			}
			if v.InSenderHistory {
				offlineSenderHistValid++
			}
			if v.InReceiverConvList {
				offlineReceiverConvValid++
			}
			if v.InReceiverHistory {
				offlineReceiverHistValid++
			}

			// 离线用户全链路验证
			if v.MessageSent &&
				v.InSenderConvList && v.InSenderHistory &&
				v.InReceiverConvList && v.InReceiverHistory {
				fullChainValid++
			}
		}

		if v.Error != "" && len(validationErrors) < 20 {
			validationErrors = append(validationErrors, v.Error)
		}
	}

	// 系统资源统计
	var peakMemory, avgMemory, peakGoroutines float64
	var avgGoroutines, totalGC uint32

	if len(systemMetrics) > 0 {
		peakMemory = systemMetrics[0].MemoryUsageMB
		for _, m := range systemMetrics {
			avgMemory += m.MemoryUsageMB
			avgGoroutines += uint32(m.GoroutineCount)
			if m.MemoryUsageMB > peakMemory {
				peakMemory = m.MemoryUsageMB
			}
			if float64(m.GoroutineCount) > peakGoroutines {
				peakGoroutines = float64(m.GoroutineCount)
			}
		}
		avgMemory /= float64(len(systemMetrics))
		avgGoroutines /= uint32(len(systemMetrics))
		totalGC = systemMetrics[len(systemMetrics)-1].NumGC - systemMetrics[0].NumGC
	}

	// 输出完整测试报告
	t.Log("")
	t.Log("========================================")
	t.Log("📊 真实场景压测报告")
	t.Log("========================================")
	t.Log("")
	t.Log("🔌 连接统计")
	t.Logf("  目标用户数:   %d", totalUsers)
	t.Logf("  尝试连接:     %d", totalConn)
	t.Logf("  成功连接:     %d (%.1f%%)", successConn, connSuccessRate)
	t.Logf("  失败连接:     %d", failedConn)
	t.Logf("  重新上线:     %d (%.1f%%)", reconnections, float64(reconnections)*100/float64(max(int64(totalUsers), 1)))
	t.Logf("  峰值在线:     %d 用户", peak)
	t.Log("")
	t.Log("💬 消息统计")
	t.Logf("  发送消息:     %d", sentMsg)
	t.Logf("  成功消息:     %d (%.1f%%)", successMsg, msgSuccessRate)
	t.Logf("  失败消息:     %d", failedMsg)
	t.Logf("  首条限制:     %d", blockedMsg)
	t.Logf("  接收消息:     %d", recvMsg)
	t.Log("")
	t.Log("📱 上下线场景")
	t.Logf("  离线消息推送: %d 条", offlineMsgRecv)
	t.Logf("  在线状态推送: %d 次", onlineStatusRecv)
	t.Log("")
	t.Log("⚡ 性能指标")
	t.Logf("  总耗时:       %v", totalDuration.Round(time.Second))
	t.Logf("  QPS:          %.2f 消息/秒", qps)
	t.Logf("  平均延迟:     %v", avgLatency)
	t.Logf("  P50 延迟:     %v", p50Latency)
	t.Logf("  P95 延迟:     %v", p95Latency)
	t.Logf("  P99 延迟:     %v", p99Latency)
	t.Logf("  最大延迟:     %v", maxLatency)
	t.Log("")
	t.Log("📡 网络流量")
	t.Logf("  发送字节:     %.2f MB", float64(bytesSent)/1024/1024)
	t.Logf("  接收字节:     %.2f MB", float64(bytesRecv)/1024/1024)
	t.Logf("  总流量:       %.2f MB", float64(bytesSent+bytesRecv)/1024/1024)
	t.Logf("  平均带宽:     %.2f MB/s", bandwidth)
	t.Log("")
	t.Log("💾 系统资源")
	t.Logf("  CPU 核心:     %d", runtime.NumCPU())
	t.Logf("  峰值内存:     %.2f MB", peakMemory)
	t.Logf("  平均内存:     %.2f MB", avgMemory)
	t.Logf("  峰值协程:     %.0f", peakGoroutines)
	t.Logf("  平均协程:     %d", avgGoroutines)
	t.Logf("  GC 次数:      %d", totalGC)
	t.Log("")
	t.Log("✅ 数据验证（全链路闭环）")
	t.Logf("  验证样本:     %d 条消息 (在线 %d 条, 离线 %d 条)", totalValidations, onlineCount, offlineCount)
	t.Log("")

	// 在线用户验证结果
	if onlineCount > 0 {
		t.Log("  【在线用户验证】")
		t.Logf("    发送确认:           %d/%d (%.1f%%)", onlineSentValid, onlineCount,
			float64(onlineSentValid)*100/float64(onlineCount))
		t.Logf("    接收方WS消息推送:   %d/%d (%.1f%%)", onlineRecvMsgValid, onlineCount,
			float64(onlineRecvMsgValid)*100/float64(onlineCount))
		t.Logf("    接收方WS会话更新:   %d/%d (%.1f%%)", onlineRecvConvUpdValid, onlineCount,
			float64(onlineRecvConvUpdValid)*100/float64(onlineCount))
		t.Logf("    发送方会话列表:     %d/%d (%.1f%%)", onlineSenderConvValid, onlineCount,
			float64(onlineSenderConvValid)*100/float64(onlineCount))
		t.Logf("    发送方消息历史:     %d/%d (%.1f%%)", onlineSenderHistValid, onlineCount,
			float64(onlineSenderHistValid)*100/float64(onlineCount))
		t.Logf("    接收方会话列表:     %d/%d (%.1f%%)", onlineReceiverConvValid, onlineCount,
			float64(onlineReceiverConvValid)*100/float64(onlineCount))
		t.Logf("    接收方消息历史:     %d/%d (%.1f%%)", onlineReceiverHistValid, onlineCount,
			float64(onlineReceiverHistValid)*100/float64(onlineCount))
		t.Logf("    接收方未读计数:     %d/%d (%.1f%%)", onlineReceiverUnreadValid, onlineCount,
			float64(onlineReceiverUnreadValid)*100/float64(onlineCount))
		t.Log("")
	}

	// 离线用户验证结果
	if offlineCount > 0 {
		t.Log("  【离线用户验证】")
		t.Logf("    发送确认:           %d/%d (%.1f%%)", offlineSentValid, offlineCount,
			float64(offlineSentValid)*100/float64(offlineCount))
		t.Logf("    发送方会话列表:     %d/%d (%.1f%%)", offlineSenderConvValid, offlineCount,
			float64(offlineSenderConvValid)*100/float64(offlineCount))
		t.Logf("    发送方消息历史:     %d/%d (%.1f%%)", offlineSenderHistValid, offlineCount,
			float64(offlineSenderHistValid)*100/float64(offlineCount))
		t.Logf("    接收方会话列表:     %d/%d (%.1f%%)", offlineReceiverConvValid, offlineCount,
			float64(offlineReceiverConvValid)*100/float64(offlineCount))
		t.Logf("    接收方消息历史:     %d/%d (%.1f%%)", offlineReceiverHistValid, offlineCount,
			float64(offlineReceiverHistValid)*100/float64(offlineCount))
		t.Log("")
	}

	// 总体全链路通过率
	if totalValidations > 0 {
		t.Logf("  全链路通过:         %d/%d (%.1f%%)", fullChainValid, totalValidations,
			float64(fullChainValid)*100/float64(totalValidations))
	}

	if len(validationErrors) > 0 {
		t.Log("")
		t.Log("❌ 验证错误（前 20 个）")
		for i, err := range validationErrors {
			t.Logf("  %d. %s", i+1, err)
		}
	}

	t.Log("")
	t.Log("========================================")

	// 断言
	passed := true

	if connSuccessRate < 85 {
		t.Errorf("❌ 连接成功率太低: %.1f%% (期望 >= 85%%)", connSuccessRate)
		passed = false
	} else {
		t.Logf("✅ 连接成功率: %.1f%%", connSuccessRate)
	}

	if msgSuccessRate < 70 {
		t.Errorf("❌ 消息成功率太低: %.1f%% (期望 >= 70%%)", msgSuccessRate)
		passed = false
	} else {
		t.Logf("✅ 消息成功率: %.1f%%", msgSuccessRate)
	}

	if avgLatency > 2*time.Second {
		t.Errorf("❌ 平均延迟太高: %v (期望 < 2s)", avgLatency)
		passed = false
	} else {
		t.Logf("✅ 平均延迟: %v", avgLatency)
	}

	if p95Latency > 5*time.Second {
		t.Errorf("❌ P95 延迟太高: %v (期望 < 5s)", p95Latency)
		passed = false
	} else {
		t.Logf("✅ P95 延迟: %v", p95Latency)
	}

	if totalValidations > 0 {
		fullChainRate := float64(fullChainValid) * 100 / float64(totalValidations)
		if fullChainRate < 70 {
			t.Errorf("❌ 全链路验证通过率太低: %.1f%% (期望 >= 70%%)", fullChainRate)
			passed = false
		} else {
			t.Logf("✅ 全链路验证通过率: %.1f%%", fullChainRate)
		}
	}

	if passed {
		t.Log("")
		t.Log("🎉 压测通过！系统表现优秀！")
	} else {
		t.Log("")
		t.Log("⚠️  压测发现问题，请优化后重试")
	}
}

// verifyInConversationList 验证消息是否出现在会话列表中
func verifyInConversationList(token, conversationID string) bool {
	resp, body, err := httpRequest("GET", "/api/conversations?limit=50", token, nil)
	if err != nil || resp.StatusCode != 200 {
		return false
	}

	result := parseResponse(body)
	conversations, ok := result["conversations"].([]interface{})
	if !ok {
		return false
	}

	for _, conv := range conversations {
		c, ok := conv.(map[string]interface{})
		if !ok {
			continue
		}
		if id, ok := c["id"].(string); ok && id == conversationID {
			return true
		}
	}
	return false
}

// verifyInMessageHistory 验证消息是否出现在消息历史中
func verifyInMessageHistory(token, conversationID, messageID string) bool {
	if messageID == "" {
		return false
	}

	resp, body, err := httpRequest("GET", fmt.Sprintf("/api/conversations/%s/messages?limit=50", conversationID), token, nil)
	if err != nil || resp.StatusCode != 200 {
		return false
	}

	result := parseResponse(body)
	messages, ok := result["messages"].([]interface{})
	if !ok {
		return false
	}

	for _, msg := range messages {
		m, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}
		if id, ok := m["id"].(string); ok && id == messageID {
			return true
		}
	}
	return false
}

// verifyUnreadCount 验证未读计数是否 > 0
func verifyUnreadCount(token, conversationID string) bool {
	resp, body, err := httpRequest("GET", "/api/conversations?limit=50", token, nil)
	if err != nil || resp.StatusCode != 200 {
		return false
	}

	result := parseResponse(body)
	conversations, ok := result["conversations"].([]interface{})
	if !ok {
		return false
	}

	for _, conv := range conversations {
		c, ok := conv.(map[string]interface{})
		if !ok {
			continue
		}
		if id, ok := c["id"].(string); ok && id == conversationID {
			// 检查未读计数是否 > 0
			if unread, ok := c["unread_count"].(float64); ok && unread > 0 {
				return true
			}
		}
	}
	return false
}

// containsString 检查字符串是否包含子串（简单版本）
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// max 返回两个 int64 的最大值
func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// getEnvInt 从环境变量获取整数配置，如果不存在则返回默认值

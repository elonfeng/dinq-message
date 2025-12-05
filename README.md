# dinq_message - 企业级即时通讯系统

一个基于 Go + WebSocket + PostgreSQL + Redis 构建的高性能、可扩展的即时通讯系统，支持私聊、群聊、离线消息、消息撤回、拉黑等完整功能。

## 目录

- [系统架构](#系统架构)
- [核心功能](#核心功能)
- [技术栈](#技术栈)
- [关键技术点](#关键技术点)
- [项目结构](#项目结构)
- [快速开始](#快速开始)
- [性能指标](#性能指标)
- [API 文档](#api-文档)

---

## 系统架构

### 整体架构图

```
                   ┌─────────────────────────────────────────┐
                   │         Client (Web/Mobile)              │
                   └─────────────────────────────────────────┘
                            │              │
                            │              │
        ┌───────────────────┤              └─────────────────┐
        │ ①登录获取JWT      │                                │
        │                   ▼                                │
        │           ┌──────────────┐                         │
        │           │   Gateway    │                         │
        │           │  (port 8080) │                         │
        │           └──────────────┘                         │
        │                   │                                │
        │ ②请求ws_token     │                                │
        │  (携带JWT)        │                                │
        │                   │                                │
        └──────────────────►│                                │
                            │                                │
             ③返回{ws_token, ws_url}                         │
             ws_url = "ws://localhost:8083/ws"              │
             (Gateway告知dinq_message地址)                   │
                            │                                │
                            │     ④客户端直连dinq_message     │
                            │     (携带ws_token)              │
                            │     ws://localhost:8083/ws?token=xxx
                            │                                │
                            │                                ▼
                            │                    ┌──────────────────────┐
                            │                    │   dinq_message       │
                            │                    │    (port 8083)       │
                            │                    ├──────────────────────┤
                            │                    │  ⑤校验ws_token       │
                            │                    │  (JWT签名+过期时间)  │
                            │                    │  middleware.         │
                            │                    │  ValidateToken()     │
                            │                    ├──────────────────────┤
                            │                    │  ┌────────────────┐  │
                            │                    │  │ WebSocket Hub  │  │
                            │                    │  │ - 连接管理      │  │
                            │                    │  │ - 消息路由      │  │
                            │                    │  │ - 心跳检测      │  │
                            │                    │  └────────────────┘  │
                            │                    │                      │
             ⑥HTTP API请求   │                    │  ┌────────────────┐  │
            (携带JWT)       │                    │  │  HTTP API      │  │
                            └───────────────────►│  │ - 会话管理      │  │
                                                 │  │ - 消息历史      │  │
                                                 │  │ - 通知管理      │  │
                                                 │  └────────────────┘  │
                                                 │                      │
                                                 │  ┌────────────────┐  │
                                                 │  │  Services      │  │
                                                 │  │ - Message      │  │
                                                 │  │ - Conversation │  │
                                                 │  │ - Notification │  │
                                                 │  └────────────────┘  │
                                                 └──────────────────────┘
                                                           │
                                        ┌──────────────────┼──────────────────┐
                                        ▼                  ▼                  ▼
                                ┌──────────────┐  ┌──────────────┐  ┌──────────────┐
                                │  PostgreSQL  │  │    Redis     │  │   Gateway    │
                                │   (消息)      │  │  (缓存/离线)  │  │ (用户认证)    │
                                └──────────────┘  └──────────────┘  └──────────────┘
```

**关键流程说明**:

1. **用户登录**: 客户端向 Gateway (port 8080) 发送登录请求，获取 JWT Token
2. **获取 ws_token**: 客户端携带 JWT 向 Gateway 请求 `/message/ws-token`，获取临时 WebSocket Token
3. **返回连接信息**: Gateway 返回 `{ws_token, ws_url, expires_in}`，告知客户端 dinq_message 的地址
   ```json
   {
     "ws_token": "eyJhbGc...",
     "ws_url": "ws://localhost:8083/ws",
     "expires_in": "300"
   }
   ```
4. **WebSocket 直连**: 客户端使用 ws_token 直接连接 dinq_message (`ws://localhost:8083/ws?token=xxx`)
5. **Token 校验**: dinq_message 的 `middleware.ValidateToken()` 验证 ws_token 的 JWT 签名和有效期（5分钟），校验通过后建立 WebSocket 连接（见 handler/websocket.go:217）
6. **HTTP API 调用**: 非实时操作（如查询历史消息、会话列表）通过 Gateway 转发到 dinq_message 的 HTTP API

**架构优势**:
- **WebSocket 直连**: 减少 Gateway 转发开销，降低实时消息延迟
- **认证分离**: Gateway 负责签发临时 Token，dinq_message 负责校验 Token 和业务逻辑
- **安全性**: ws_token 有效期仅 5 分钟，限制了 Token 泄露的风险
- **负载均衡**: Gateway 可以返回不同的 ws_url，将 WebSocket 连接分发到多个 dinq_message 实例

### 核心组件说明

#### 1. WebSocket Hub（连接管理中心）
- **职责**: 管理所有 WebSocket 连接的生命周期
- **核心功能**:
  - 用户连接注册与注销
  - 消息路由与广播
  - 在线状态维护
  - 心跳检测（30秒超时）
- **数据结构**:
  ```go
  type Hub struct {
      clients    map[uuid.UUID]*Client  // 用户ID -> 连接
      broadcast  chan []byte            // 广播通道
      register   chan *Client           // 连接注册
      unregister chan *Client           // 连接注销
  }
  ```

#### 2. Message Service（消息服务）
- **职责**: 消息的创建、存储、查询、撤回
- **核心流程**:
  1. 验证发送者权限（拉黑检查、首条消息限制）
  2. 创建/查找会话
  3. 保存消息到数据库
  4. 推送给在线用户（WebSocket）
  5. 离线消息存储到 Redis

#### 3. Notification Service（通知服务）
- **职责**: 系统通知、消息通知的创建与推送
- **通知类型**:
  - `system`: 系统公告
  - `message`: 消息通知（目前已禁用，改用未读计数）
  - `card_completed`: 卡片完成通知
  - `custom`: 自定义通知
- **推送机制**: 通过 WebSocket 实时推送 + 持久化存储

#### 4. System Settings Service（系统配置服务）
- **职责**: 管理系统级别的可配置项
- **配置项**:
  - `enable_first_message_limit`: 首条消息限制开关
  - `max_video_size_mb`: 视频文件大小限制
- **特性**: 内存缓存 + 数据库持久化，支持热更新

---

## 核心功能

### 1. 私聊与群聊

#### 私聊
- 自动创建会话（首次发消息时）
- 首条消息限制（防骚扰）
- 在线状态显示
- 未读计数实时更新

#### 群聊
- 支持创建群聊（提供群名和成员列表）
- 成员管理（添加、移除、离开）
- 角色管理（owner、admin、member）
- 权限控制（只有 owner/admin 可添加/移除成员）

### 2. 消息类型

支持多种消息类型：
- `text`: 文本消息
- `image`: 图片消息
- `video`: 视频消息
- `emoji`: 表情消息

### 3. 消息撤回

**限制条件**:
- 只能撤回 2 分钟内的消息
- 只能撤回自己发送的消息

**撤回流程**:
1. 验证消息所有权和时间限制
2. 标记数据库中的消息为已撤回
3. 通过 WebSocket 广播撤回事件给所有会话成员

### 4. 首条消息限制（防骚扰机制）

**业务规则**:
- 用户 A 首次给用户 B 发消息后，在 B 回复之前，A 不能发送第二条消息
- 该功能可通过系统配置全局开启/关闭

**实现机制**:
```go
// 检查首条消息限制
func (s *MessageService) checkFirstMessageLimit(senderID, receiverID uuid.UUID) error {
    // 查询会话中最后一条消息
    var lastMsg model.Message
    err := s.db.Where("conversation_id = ?", conversationID).
        Order("created_at DESC").
        First(&lastMsg).Error

    // 如果最后一条消息是 sender 发的，且 receiver 还未回复
    if lastMsg.SenderID == senderID {
        return errors.New("首条消息限制: 等待对方回复")
    }
    return nil
}
```

### 5. 拉黑功能

**效果**:
- 用户 A 拉黑 B 后，B 无法给 A 发送消息
- B 尝试发送消息时会收到 `"you are blocked by this user"` 错误
- 支持取消拉黑

**数据模型**:
```go
type Relationship struct {
    UserID         uuid.UUID  // 拉黑者
    TargetUserID   uuid.UUID  // 被拉黑者
    RelationshipType string   // "blocked"
}
```

### 6. 离线消息

**存储机制**:
- 在线用户：直接通过 WebSocket 推送
- 离线用户：消息存储到 Redis 队列 `offline_messages:{user_id}`

**接收机制**:
- 用户上线后，自动接收所有离线消息
- 接收后从 Redis 队列中删除

### 7. 未读计数

**实时更新**:
- 每当有新消息时，自动增加接收者的未读计数
- 通过 WebSocket 推送 `unread_count_update` 事件
- 消息格式:
  ```json
  {
    "type": "unread_count_update",
    "data": {
      "conversation_id": "uuid",
      "unread_count": 5
    }
  }
  ```

**标记已读**:
- 发送 `read` 类型的 WebSocket 消息
- 未读计数归零

### 8. 已读回执

**可选功能**，通过会话设置控制：
- 用户可关闭已读回执功能
- 其他用户发送已读回执时，如果该用户关闭了此功能，则不会收到回执

### 9. 正在输入提示

**实时广播**:
- 用户输入时，发送 `typing` 类型消息
- 广播给会话中的其他在线成员
- 前端显示 "对方正在输入..."

---

## 技术栈

### 后端核心
- **语言**: Go 1.25+
- **Web框架**: Gin
- **WebSocket**: gorilla/websocket
- **ORM**: GORM
- **数据库**: PostgreSQL 14+
- **缓存**: Redis 7+
- **认证**: JWT (golang-jwt/jwt)

### 依赖库
```go
require (
    github.com/gin-gonic/gin v1.11.0
    github.com/gorilla/websocket v1.5.3
    github.com/google/uuid v1.6.0
    github.com/golang-jwt/jwt/v5 v5.3.0
    github.com/redis/go-redis/v9 v9.14.1
    github.com/joho/godotenv v1.5.1
    gorm.io/gorm v1.31.0
    gorm.io/driver/postgres v1.6.0
)
```

---

## 关键技术点

### 1. WebSocket 连接管理

#### 连接池设计
```go
type Hub struct {
    clients    map[uuid.UUID]*Client  // 用户ID到连接的映射
    clientsMu  sync.RWMutex            // 读写锁保护并发访问
    broadcast  chan []byte             // 广播通道
    register   chan *Client            // 注册通道
    unregister chan *Client            // 注销通道
}
```

#### 心跳机制
- **客户端**: 每 30 秒发送一次心跳 `{"type": "heartbeat"}`
- **服务端**: 检测超时连接并自动断开
- **目的**: 及时清理僵尸连接，维护准确的在线状态

#### 优雅关闭
```go
func (c *Client) Close() {
    c.once.Do(func() {
        close(c.Send)  // 关闭发送通道
        c.conn.Close() // 关闭底层连接
    })
}
```

### 2. 消息可靠性保证

#### 消息持久化
- **写入顺序**: 先写数据库，再推送 WebSocket
- **事务保护**: 使用数据库事务确保消息表和会话表的一致性

```go
func (s *MessageService) SendMessage(msg *model.Message) error {
    return s.db.Transaction(func(tx *gorm.DB) error {
        // 1. 保存消息
        if err := tx.Create(msg).Error; err != nil {
            return err
        }

        // 2. 更新会话的 last_message_id 和 last_message_at
        if err := tx.Model(&model.Conversation{}).
            Where("id = ?", msg.ConversationID).
            Updates(map[string]interface{}{
                "last_message_id": msg.ID,
                "last_message_at": time.Now(),
            }).Error; err != nil {
            return err
        }

        return nil
    })
}
```

#### 离线消息队列
- **Redis List**: 使用 `LPUSH` 存储离线消息
- **队列长度限制**: 每个用户最多存储 1000 条离线消息
- **TTL**: 离线消息保留 7 天

### 3. 并发控制

#### 读写锁（RWMutex）
```go
// 读操作（多个goroutine可同时读）
h.clientsMu.RLock()
client, exists := h.clients[userID]
h.clientsMu.RUnlock()

// 写操作（独占访问）
h.clientsMu.Lock()
h.clients[userID] = client
h.clientsMu.Unlock()
```

#### 消息通道（Channel）
- **注册通道**: 新连接注册
- **注销通道**: 连接断开清理
- **广播通道**: 消息广播
- **好处**: 避免锁竞争，提高并发性能

### 4. 性能优化

#### 数据库查询优化
- **索引优化**:
  ```sql
  CREATE INDEX idx_messages_conversation ON messages(conversation_id, created_at);
  CREATE INDEX idx_messages_sender ON messages(sender_id);
  CREATE INDEX idx_conversation_members ON conversation_members(user_id, conversation_id);
  ```
- **避免 N+1 查询**: 使用 `Preload` 预加载关联数据
  ```go
  db.Preload("Members").Find(&conversations)
  ```

#### Redis 缓存策略
- **在线状态缓存**: `online_users` Set
- **未读计数缓存**: `unread_count:{user_id}:{conversation_id}` String
- **离线消息队列**: `offline_messages:{user_id}` List
- **TTL 策略**: 在线状态 5 分钟 TTL，自动过期

#### 消息分页
- **历史消息**: 每次加载 50 条，按 `created_at DESC` 排序
- **通知列表**: 每次加载 50 条，支持 `unread_only` 过滤

### 5. 安全机制

#### JWT 认证
```go
// HTTP API 认证
func AuthMiddleware() gin.HandlerFunc {
    return func(c *gin.Context) {
        tokenString := c.GetHeader("Authorization")
        token, err := jwt.Parse(tokenString, keyFunc)
        if err != nil {
            c.AbortWithStatus(401)
            return
        }
        c.Set("user_id", claims.UserID)
        c.Next()
    }
}

// WebSocket 认证（通过 URL 参数）
ws_token := c.Query("token")
```

#### 权限验证
- **群聊操作**: 验证用户角色（owner/admin/member）
- **消息撤回**: 验证消息所有权
- **管理员API**: 验证管理员权限

#### 输入校验
- **消息长度限制**: 文本消息最大 10000 字符
- **视频大小限制**: 默认 50MB，可配置
- **防止 XSS**: 前端需对用户输入进行转义

### 6. 可扩展性设计

#### 微服务架构
```
Gateway (认证/路由)
   ↓
dinq_message (消息服务)
   ↓
PostgreSQL (持久化) + Redis (缓存)
```

#### 水平扩展支持
- **无状态设计**: 每个 dinq_message 实例独立运行
- **Redis Pub/Sub**: 支持跨实例消息广播（未来扩展）
- **负载均衡**: 可通过 Nginx/HAProxy 进行负载均衡

#### 配置热更新
```go
// 管理员可通过 API 动态修改系统配置
POST /api/admin/settings/enable_first_message_limit
{"value": "false"}

// 配置立即生效，无需重启服务
POST /api/admin/settings/reload
```

---

## 项目结构

```
dinq_message/
├── main.go                 # 程序入口
├── go.mod                  # Go模块依赖
├── go.sum                  # 依赖校验
├── .env.example            # 环境变量示例
│
├── config/                 # 配置管理
│   └── config.go           # 配置加载
│
├── model/                  # 数据模型
│   ├── user.go             # 用户模型
│   ├── conversation.go     # 会话模型
│   ├── message.go          # 消息模型
│   ├── notification.go     # 通知模型
│   └── relationship.go     # 用户关系模型
│
├── handler/                # 请求处理
│   ├── websocket.go        # WebSocket处理（Hub + Client）
│   ├── conversation.go     # 会话管理接口
│   ├── notification.go     # 通知管理接口
│   ├── relationship.go     # 关系管理接口
│   └── system_settings.go # 系统配置接口
│
├── service/                # 业务逻辑
│   ├── message_service.go       # 消息服务
│   ├── conversation_service.go  # 会话服务
│   ├── notification_service.go  # 通知服务
│   └── system_settings_service.go # 系统配置服务
│
├── middleware/             # 中间件
│   ├── auth.go             # JWT认证
│   └── error_handler.go    # 统一错误处理
│
├── utils/                  # 工具函数
│   ├── db.go               # 数据库连接
│   └── redis.go            # Redis连接
│
├── test/                   # 测试文件
│   ├── helpers_test.go     # 测试辅助函数
│   ├── basic_chat_test.go  # 基础聊天测试
│   ├── advanced_features_test.go # 高级功能测试
│   ├── edge_cases_test.go  # 边界测试
│   └── performance_test.go # 性能测试
│
├── API.md                  # API文档
└── README.md               # 项目说明
```

---

## 快速开始

### 1. 环境要求

- Go 1.25+
- PostgreSQL 14+
- Redis 7+

### 2. 安装依赖

```bash
go mod download
```

### 3. 配置环境变量

创建 `.env` 文件：

```bash
# 服务配置
PORT=8083
JWT_SECRET=your-secret-key-here

# 数据库配置
DATABASE_URL=postgres://user:password@localhost:5432/dinq_message?sslmode=disable

# Redis配置
REDIS_URL=localhost:6379
REDIS_PASSWORD=
REDIS_DB=0

# 系统配置
MAX_VIDEO_SIZE_MB=50
```

### 4. 初始化数据库

```sql
CREATE DATABASE dinq_message;
```

数据库表会在首次启动时自动创建（通过 GORM AutoMigrate）。

### 5. 启动服务

```bash
go run main.go
```

服务启动在 `http://localhost:8083`

### 6. 测试连接

```bash
# 健康检查
curl http://localhost:8083/health

# 响应: {"status":"ok"}
```

---

## 性能指标

### 测试环境
- **硬件**: MacBook Pro M1, 16GB RAM
- **数据库**: PostgreSQL 14 (本地)
- **Redis**: Redis 7 (本地)

### 基准测试结果

#### 1. 并发连接测试
```bash
go test -v -run TestPerformance_ConcurrentMessages ./test
```

**结果**:
- **并发用户**: 100 对用户（200个WebSocket连接）
- **消息总数**: 200 条（每对用户互发1条）
- **成功率**: 100%
- **平均耗时**: < 5 秒

#### 2. WebSocket 容量测试
```bash
go test -v -run TestPerformance_WebSocketCapacity ./test
```

**结果**:
- **并发连接**: 500 个 WebSocket 连接
- **消息总数**: 2500 条（每个连接发送5条）
- **成功率**: > 95%
- **QPS**: 约 800-1000 条/秒
- **平均延迟**: < 50ms
- **P95延迟**: < 100ms

#### 3. 高吞吐量测试
```bash
go test -v -run TestPerformance_HighThroughput ./test
```

**结果**:
- **消息总数**: 5000 条（100用户 × 50条）
- **TPS**: 约 500-700 条/秒
- **错误率**: < 1%

### 性能建议

1. **生产环境优化**:
   - 使用连接池（数据库、Redis）
   - 开启 GORM 的 PreparedStatement
   - 配置合适的 Redis 内存淘汰策略

2. **扩展建议**:
   - 单机可支持 5000+ 并发连接
   - 水平扩展可通过 Redis Pub/Sub 跨实例通信
   - 数据库分库分表（按会话ID哈希）

---

## API 文档

详细的 API 文档请参考 [API.md](./API.md)

### 核心接口概览

#### WebSocket
- `GET /ws?token=<ws_token>` - 建立 WebSocket 连接

#### 会话管理
- `GET /api/conversations` - 获取会话列表
- `POST /api/conversations/group` - 创建群聊
- `GET /api/conversations/:id/messages` - 获取消息历史

#### 群聊管理
- `POST /api/conversations/:id/members` - 添加成员
- `POST /api/conversations/:id/members/remove` - 移除成员
- `POST /api/conversations/:id/leave` - 离开群聊
- `POST /api/conversations/:id/members/:user_id/role` - 更新成员角色

#### 消息管理
- `POST /api/messages/:id/recall` - 撤回消息

#### 通知管理
- `GET /api/notifications` - 获取通知列表
- `POST /api/notifications/:id/read` - 标记已读
- `POST /api/notifications/read-all` - 全部已读
- `POST /api/notifications/:id/delete` - 删除通知

#### 关系管理
- `POST /api/relationships/block` - 拉黑用户
- `POST /api/relationships/unblock` - 取消拉黑
- `GET /api/relationships/blocked` - 获取拉黑列表

#### 管理员API
- `GET /api/admin/settings` - 获取系统设置
- `POST /api/admin/settings/:key` - 更新系统设置
- `GET /api/admin/notification-templates` - 通知模板管理

---

## 测试

### 运行所有测试
```bash
go test -v ./test
```

### 运行特定测试
```bash
# 基础聊天测试
go test -v -run TestBasicChat ./test

# 高级功能测试
go test -v -run TestAdvanced ./test

# 性能测试（跳过压力测试）
go test -v -short -run TestPerformance ./test

# 完整性能测试（包含500并发）
go test -v -run TestPerformance ./test
```

### 测试覆盖范围

- ✅ 基础聊天功能（私聊、群聊）
- ✅ WebSocket 连接管理（认证、心跳、断线重连）
- ✅ 消息撤回（时间限制、权限验证）
- ✅ 拉黑功能（双向验证）
- ✅ 首条消息限制（防骚扰）
- ✅ 离线消息（队列存储、上线接收）
- ✅ 未读计数（实时更新、标记已读）
- ✅ 群聊管理（成员增删、角色管理）
- ✅ 通知系统（创建、推送、已读）
- ✅ 边界情况（无效JSON、超长消息、重复操作）
- ✅ 性能测试（并发连接、高吞吐、事务一致性）

---

## 未来规划

### 短期目标
- [ ] 消息加密（端到端加密）
- [ ] 文件上传支持（集成 OSS）
- [ ] 语音/视频通话（集成 WebRTC）
- [ ] 消息搜索（Elasticsearch）

### 长期目标
- [ ] 消息已读状态（群聊场景）
- [ ] 群聊 @ 提及功能
- [ ] 消息引用/转发
- [ ] 会话置顶/静音
- [ ] 聊天记录导出
- [ ] 多设备同步

---

## 许可证

MIT License

---

## 贡献者

- Allen - 架构设计与核心开发

---

## 联系方式

如有问题或建议，请提交 Issue 或 Pull Request。

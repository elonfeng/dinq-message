# DINQ System - dinq-message

> This is the **Inbox** module of the DINQ microservices system, handling real-time messaging and notifications.

---

## dinq-message - Enterprise Instant Messaging System

A high-performance, scalable instant messaging system built with Go + WebSocket + PostgreSQL + Redis, supporting private chat, group chat, offline messages, message recall, blocking, and more.

## Table of Contents

- [System Architecture](#system-architecture)
- [Core Features](#core-features)
- [Tech Stack](#tech-stack)
- [Key Technical Points](#key-technical-points)
- [Project Structure](#project-structure)
- [Quick Start](#quick-start)
- [Performance Metrics](#performance-metrics)

---

## System Architecture

### Architecture Diagram

```
                   ┌─────────────────────────────────────────┐
                   │         Client (Web/Mobile)              │
                   └─────────────────────────────────────────┘
                            │              │
                            │              │
        ┌───────────────────┤              └─────────────────┐
        │ ①Login get JWT    │                                │
        │                   ▼                                │
        │           ┌──────────────┐                         │
        │           │   Gateway    │                         │
        │           │  (port 8080) │                         │
        │           └──────────────┘                         │
        │                   │                                │
        │ ②Request ws_token │                                │
        │  (with JWT)       │                                │
        │                   │                                │
        └──────────────────►│                                │
                            │                                │
             ③Return {ws_token, ws_url}                      │
             ws_url = "ws://localhost:8083/ws"              │
             (Gateway provides dinq_message address)        │
                            │                                │
                            │     ④Client connects directly  │
                            │     (with ws_token)            │
                            │     ws://localhost:8083/ws?token=xxx
                            │                                │
                            │                                ▼
                            │                    ┌──────────────────────┐
                            │                    │   dinq_message       │
                            │                    │    (port 8083)       │
                            │                    ├──────────────────────┤
                            │                    │  ⑤Validate ws_token  │
                            │                    │  (JWT signature+TTL) │
                            │                    │  middleware.         │
                            │                    │  ValidateToken()     │
                            │                    ├──────────────────────┤
                            │                    │  ┌────────────────┐  │
                            │                    │  │ WebSocket Hub  │  │
                            │                    │  │ - Connection   │  │
                            │                    │  │ - Routing      │  │
                            │                    │  │ - Heartbeat    │  │
                            │                    │  └────────────────┘  │
                            │                    │                      │
             ⑥HTTP API      │                    │  ┌────────────────┐  │
            (with JWT)      │                    │  │  HTTP API      │  │
                            └───────────────────►│  │ - Conversation │  │
                                                 │  │ - History      │  │
                                                 │  │ - Notification │  │
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
                                │  (messages)  │  │ (cache/offline)│ │   (auth)     │
                                └──────────────┘  └──────────────┘  └──────────────┘
```

**Key Flow**:

1. **User Login**: Client sends login request to Gateway (port 8080), receives JWT Token
2. **Get ws_token**: Client requests `/message/ws-token` with JWT, gets temporary WebSocket Token
3. **Return Connection Info**: Gateway returns `{ws_token, ws_url, expires_in}`
   ```json
   {
     "ws_token": "eyJhbGc...",
     "ws_url": "ws://localhost:8083/ws",
     "expires_in": "300"
   }
   ```
4. **WebSocket Direct Connect**: Client connects directly to dinq_message (`ws://localhost:8083/ws?token=xxx`)
5. **Token Validation**: dinq_message's `middleware.ValidateToken()` validates JWT signature and TTL (5 minutes)
6. **HTTP API Calls**: Non-realtime operations (history, conversation list) go through Gateway to dinq_message HTTP API

**Architecture Benefits**:
- **Direct WebSocket**: Reduces Gateway overhead, lowers latency
- **Separated Auth**: Gateway issues temporary tokens, dinq_message validates and handles business logic
- **Security**: ws_token expires in 5 minutes, limiting token leak risk
- **Load Balancing**: Gateway can return different ws_url to distribute connections

### Core Components

#### 1. WebSocket Hub (Connection Manager)
- **Role**: Manage all WebSocket connection lifecycles
- **Functions**:
  - User connection registration/deregistration
  - Message routing and broadcasting
  - Online status maintenance
  - Heartbeat detection (30s timeout)
- **Data Structure**:
  ```go
  type Hub struct {
      clients    map[uuid.UUID]*Client  // UserID -> Connection
      broadcast  chan []byte            // Broadcast channel
      register   chan *Client           // Registration channel
      unregister chan *Client           // Deregistration channel
  }
  ```

#### 2. Message Service
- **Role**: Message creation, storage, retrieval, recall
- **Flow**:
  1. Validate sender permissions (block check, first message limit)
  2. Create/find conversation
  3. Save message to database
  4. Push to online users (WebSocket)
  5. Store offline messages in Redis

#### 3. Notification Service
- **Role**: System and message notification creation/push
- **Types**:
  - `system`: System announcements
  - `message`: Message notifications (disabled, using unread count instead)
  - `card_completed`: Card completion notifications
  - `custom`: Custom notifications
- **Push**: Real-time WebSocket push + persistent storage

#### 4. System Settings Service
- **Role**: Manage system-level configurations
- **Settings**:
  - `enable_first_message_limit`: First message limit toggle
  - `max_video_size_mb`: Video file size limit
- **Features**: Memory cache + database persistence, hot reload support

---

## Core Features

### 1. Private & Group Chat

#### Private Chat
- Auto-create conversation on first message
- First message limit (anti-spam)
- Online status display
- Real-time unread count updates

#### Group Chat
- Create groups with name and member list
- Member management (add, remove, leave)
- Role management (owner, admin, member)
- Permission control (only owner/admin can add/remove members)

### 2. Message Types

Supported message types:
- `text`: Text messages
- `image`: Image messages
- `video`: Video messages
- `emoji`: Emoji messages

### 3. Message Recall

**Restrictions**:
- Can only recall messages within 2 minutes
- Can only recall own messages

**Flow**:
1. Validate message ownership and time limit
2. Mark message as recalled in database
3. Broadcast recall event to all conversation members via WebSocket

### 4. First Message Limit (Anti-Spam)

**Business Rules**:
- After User A sends first message to User B, A cannot send second message until B replies
- Can be globally enabled/disabled via system config

### 5. Blocking

**Effects**:
- After User A blocks B, B cannot send messages to A
- B receives `"you are blocked by this user"` error
- Supports unblocking

### 6. Offline Messages

**Storage**:
- Online users: Direct WebSocket push
- Offline users: Stored in Redis queue `offline_messages:{user_id}`

**Retrieval**:
- Auto-receive all offline messages on login
- Deleted from Redis queue after receipt

### 7. Unread Count

**Real-time Updates**:
- Auto-increment receiver's unread count on new message
- Push `unread_count_update` event via WebSocket
- Format:
  ```json
  {
    "type": "unread_count_update",
    "data": {
      "conversation_id": "uuid",
      "unread_count": 5
    }
  }
  ```

**Mark as Read**:
- Send `read` type WebSocket message
- Unread count resets to zero

### 8. Read Receipts

**Optional feature** controlled by conversation settings:
- Users can disable read receipts
- When disabled, user won't receive read receipts from others

### 9. Typing Indicator

**Real-time Broadcast**:
- Send `typing` type message while typing
- Broadcast to other online members
- Frontend displays "typing..."

---

## Tech Stack

### Backend Core
- **Language**: Go 1.22+
- **Web Framework**: Gin
- **WebSocket**: gorilla/websocket
- **ORM**: GORM
- **Database**: PostgreSQL 14+
- **Cache**: Redis 7+
- **Auth**: JWT (golang-jwt/jwt)

### Dependencies
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

## Key Technical Points

### 1. WebSocket Connection Management

#### Connection Pool Design
```go
type Hub struct {
    clients    map[uuid.UUID]*Client  // UserID to connection mapping
    clientsMu  sync.RWMutex            // RWLock for concurrent access
    broadcast  chan []byte             // Broadcast channel
    register   chan *Client            // Register channel
    unregister chan *Client            // Unregister channel
}
```

#### Heartbeat Mechanism
- **Client**: Send heartbeat `{"type": "heartbeat"}` every 30 seconds
- **Server**: Detect timeout connections and auto-disconnect
- **Purpose**: Clean zombie connections, maintain accurate online status

#### Graceful Shutdown
```go
func (c *Client) Close() {
    c.once.Do(func() {
        close(c.Send)  // Close send channel
        c.conn.Close() // Close underlying connection
    })
}
```

### 2. Message Reliability

#### Message Persistence
- **Write Order**: Write to database first, then push WebSocket
- **Transaction Protection**: Use database transactions to ensure message and conversation table consistency

```go
func (s *MessageService) SendMessage(msg *model.Message) error {
    return s.db.Transaction(func(tx *gorm.DB) error {
        // 1. Save message
        if err := tx.Create(msg).Error; err != nil {
            return err
        }

        // 2. Update conversation's last_message_id and last_message_at
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

#### Offline Message Queue
- **Redis List**: Use `LPUSH` to store offline messages
- **Queue Limit**: Max 1000 offline messages per user
- **TTL**: 7 days retention

### 3. Concurrency Control

#### RWMutex
```go
// Read operation (multiple goroutines can read simultaneously)
h.clientsMu.RLock()
client, exists := h.clients[userID]
h.clientsMu.RUnlock()

// Write operation (exclusive access)
h.clientsMu.Lock()
h.clients[userID] = client
h.clientsMu.Unlock()
```

#### Channels
- **Register channel**: New connection registration
- **Unregister channel**: Connection cleanup
- **Broadcast channel**: Message broadcasting
- **Benefits**: Avoid lock contention, improve concurrency performance

### 4. Performance Optimization

#### Database Query Optimization
- **Index Optimization**:
  ```sql
  CREATE INDEX idx_messages_conversation ON messages(conversation_id, created_at);
  CREATE INDEX idx_messages_sender ON messages(sender_id);
  CREATE INDEX idx_conversation_members ON conversation_members(user_id, conversation_id);
  ```
- **Avoid N+1 Queries**: Use `Preload` for eager loading
  ```go
  db.Preload("Members").Find(&conversations)
  ```

#### Redis Caching Strategy
- **Online Status Cache**: `online_users` Set
- **Unread Count Cache**: `unread_count:{user_id}:{conversation_id}` String
- **Offline Message Queue**: `offline_messages:{user_id}` List
- **TTL Strategy**: 5-minute TTL for online status

#### Message Pagination
- **History Messages**: 50 per page, sorted by `created_at DESC`
- **Notification List**: 50 per page, supports `unread_only` filter

### 5. Security

#### JWT Authentication
```go
// HTTP API Authentication
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

// WebSocket Authentication (via URL parameter)
ws_token := c.Query("token")
```

#### Permission Validation
- **Group Operations**: Validate user role (owner/admin/member)
- **Message Recall**: Validate message ownership
- **Admin API**: Validate admin permissions

#### Input Validation
- **Message Length Limit**: Max 10000 characters for text
- **Video Size Limit**: Default 50MB, configurable
- **XSS Prevention**: Frontend must escape user input

### 6. Scalability

#### Microservices Architecture
```
Gateway (Auth/Routing)
   ↓
dinq_message (Messaging Service)
   ↓
PostgreSQL (Persistence) + Redis (Cache)
```

#### Horizontal Scaling
- **Stateless Design**: Each dinq_message instance runs independently
- **Redis Pub/Sub**: Cross-instance message broadcasting (future)
- **Load Balancing**: Via Nginx/HAProxy

#### Hot Config Reload
```go
// Admin can dynamically modify system config via API
POST /api/admin/settings/enable_first_message_limit
{"value": "false"}

// Config takes effect immediately, no restart needed
POST /api/admin/settings/reload
```

---

## Project Structure

```
dinq_message/
├── main.go                 # Entry point
├── go.mod                  # Go module dependencies
├── go.sum                  # Dependency checksum
├── .env.example            # Environment variable example
│
├── config/                 # Configuration
│   └── config.go           # Config loading
│
├── model/                  # Data models
│   ├── user.go             # User model
│   ├── conversation.go     # Conversation model
│   ├── message.go          # Message model
│   ├── notification.go     # Notification model
│   └── relationship.go     # User relationship model
│
├── handler/                # Request handlers
│   ├── websocket.go        # WebSocket handler (Hub + Client)
│   ├── conversation.go     # Conversation management
│   ├── notification.go     # Notification management
│   ├── relationship.go     # Relationship management
│   └── system_settings.go  # System settings
│
├── service/                # Business logic
│   ├── message_service.go       # Message service
│   ├── conversation_service.go  # Conversation service
│   ├── notification_service.go  # Notification service
│   └── system_settings_service.go # System settings service
│
├── middleware/             # Middleware
│   ├── auth.go             # JWT authentication
│   └── error_handler.go    # Unified error handling
│
├── utils/                  # Utilities
│   ├── db.go               # Database connection
│   └── redis.go            # Redis connection
│
├── test/                   # Test files
│   ├── helpers_test.go     # Test helpers
│   ├── basic_chat_test.go  # Basic chat tests
│   ├── advanced_features_test.go # Advanced feature tests
│   ├── edge_cases_test.go  # Edge case tests
│   └── performance_test.go # Performance tests
│
└── README.md               # Project documentation
```

---

## Quick Start

### 1. Requirements

- Go 1.22+
- PostgreSQL 14+
- Redis 7+

### 2. Install Dependencies

```bash
go mod download
```

### 3. Configure Environment

Create `.env` file:

```bash
# Service config
PORT=8083
JWT_SECRET=your-secret-key-here

# Database config
DATABASE_URL=postgres://user:password@localhost:5432/dinq_message?sslmode=disable

# Redis config
REDIS_URL=localhost:6379
REDIS_PASSWORD=
REDIS_DB=0

# System config
MAX_VIDEO_SIZE_MB=50
```

### 4. Initialize Database

```sql
CREATE DATABASE dinq_message;
```

Tables are auto-created on first startup (via GORM AutoMigrate).

### 5. Start Service

```bash
go run main.go
```

Service runs at `http://localhost:8083`

### 6. Test Connection

```bash
# Health check
curl http://localhost:8083/health

# Response: {"status":"ok"}
```

---

## Performance Metrics

### Test Environment
- **Hardware**: MacBook Pro M1, 16GB RAM
- **Database**: PostgreSQL 14 (local)
- **Redis**: Redis 7 (local)

### Benchmark Results

#### 1. Concurrent Connection Test
```bash
go test -v -run TestPerformance_ConcurrentMessages ./test
```

**Results**:
- **Concurrent Users**: 100 pairs (200 WebSocket connections)
- **Total Messages**: 200 (1 message per pair)
- **Success Rate**: 100%
- **Average Time**: < 5 seconds

#### 2. WebSocket Capacity Test
```bash
go test -v -run TestPerformance_WebSocketCapacity ./test
```

**Results**:
- **Concurrent Connections**: 500 WebSocket connections
- **Total Messages**: 2500 (5 messages per connection)
- **Success Rate**: > 95%
- **QPS**: ~800-1000 messages/second
- **Average Latency**: < 50ms
- **P95 Latency**: < 100ms

#### 3. High Throughput Test
```bash
go test -v -run TestPerformance_HighThroughput ./test
```

**Results**:
- **Total Messages**: 5000 (100 users × 50 messages)
- **TPS**: ~500-700 messages/second
- **Error Rate**: < 1%

### Performance Recommendations

1. **Production Optimization**:
   - Use connection pools (database, Redis)
   - Enable GORM PreparedStatement
   - Configure appropriate Redis eviction policy

2. **Scaling Recommendations**:
   - Single instance supports 5000+ concurrent connections
   - Horizontal scaling via Redis Pub/Sub
   - Database sharding by conversation ID hash

---

## Testing

### Run All Tests
```bash
go test -v ./test
```

### Run Specific Tests
```bash
# Basic chat tests
go test -v -run TestBasicChat ./test

# Advanced feature tests
go test -v -run TestAdvanced ./test

# Performance tests (skip stress tests)
go test -v -short -run TestPerformance ./test

# Full performance tests (including 500 concurrent)
go test -v -run TestPerformance ./test
```

### Test Coverage

- Basic chat (private, group)
- WebSocket management (auth, heartbeat, reconnection)
- Message recall (time limit, permission validation)
- Blocking (bidirectional validation)
- First message limit (anti-spam)
- Offline messages (queue storage, online receipt)
- Unread count (real-time updates, mark as read)
- Group management (member add/remove, role management)
- Notification system (create, push, read)
- Edge cases (invalid JSON, oversized messages, duplicate operations)
- Performance tests (concurrent connections, high throughput, transaction consistency)

---

## Future Plans

### Short-term Goals
- [ ] Message encryption (end-to-end)
- [ ] File upload support (OSS integration)
- [ ] Voice/video calls (WebRTC integration)
- [ ] Message search (Elasticsearch)

### Long-term Goals
- [ ] Read status for group messages
- [ ] Group @mentions
- [ ] Message quote/forward
- [ ] Conversation pin/mute
- [ ] Chat history export
- [ ] Multi-device sync

---

## License

MIT License

---

## Contributors

- elonfeng - Architecture design and core development

---

## Contact

For questions or suggestions, please submit an Issue or Pull Request.

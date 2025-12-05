# dinq_message API 文档

## 认证

所有 HTTP API 需要在 Header 中携带 JWT Token：
```
Authorization: Bearer <token>
```

WebSocket 连接需要在 URL 参数中携带临时 Token：
```
ws://localhost:8083/ws?token=<ws_token>
```

---

## WebSocket API

### 连接 WebSocket

**Endpoint**: `GET /ws?token=<ws_token>`

**获取 ws_token**:
```bash
# 先从 Gateway 获取 WebSocket Token
GET http://localhost:8080/message/ws-token
Authorization: Bearer <normal_jwt>

# 响应
{
  "ws_token": "eyJhbGc...",
  "ws_url": "ws://localhost:8083/ws",
  "expires_in": "300"
}
```

### WebSocket 消息格式

```json
{
  "type": "message | typing | read | heartbeat | recall",
  "data": { ... }
}
```

### 1. 发送消息

```json
{
  "type": "message",
  "data": {
    "conversation_id": "uuid",      // 可选，私聊时可不填
    "receiver_id": "uuid",           // 私聊时必填
    "message_type": "text | image | emoji",
    "content": "Hello!",
    "metadata": {                    // 可选
      "image_url": "...",
      "emoji_id": "..."
    },
    "reply_to_message_id": "uuid"   // 可选，回复消息
  }
}
```

### 2. 正在输入提示

```json
{
  "type": "typing",
  "data": {
    "conversation_id": "uuid"
  }
}
```

### 3. 已读回执

```json
{
  "type": "read",
  "data": {
    "conversation_id": "uuid",
    "message_id": "uuid"
  }
}
```

### 4. 撤回消息

```json
{
  "type": "recall",
  "data": {
    "message_id": "uuid"
  }
}
```

### 5. 心跳

```json
{
  "type": "heartbeat"
}
```

**建议每 30 秒发送一次心跳**

---

## HTTP API

### 会话管理

#### 1. 获取会话列表

```http
GET /api/conversations?limit=20&offset=0
```

**响应**:
```json
{
  "conversations": [
    {
      "id": "uuid",
      "conversation_type": "private | group",
      "group_name": "群聊名称",
      "created_at": "2025-01-19T...",
      "updated_at": "2025-01-19T...",
      "last_message_at": "2025-01-19T...",
      "last_message_id": "uuid",
      "last_message_time": "2025-01-19T10:30:00Z",
      "last_message_text": "最新消息内容预览（50字符+...）",
      "unread_count": 5,
      "online_status": {
        "user-uuid": true
      },
      "members": [
        {
          "id": "uuid",
          "user_id": "uuid",
          "role": "owner | admin | member",
          "unread_count": 5,
          "is_hidden": false,
          ...
        }
      ]
    }
  ]
}
```

**新增字段说明**:
- `last_message_time`: 最新消息时间（实时更新）
- `last_message_text`: 最新消息内容预览，不同类型显示：
  - text: 文本内容（最多50字符）
  - image: "[图片]"
  - video: "[视频]"
  - emoji: "[表情]"
- `unread_count`: 当前用户在该会话的未读数量
- `online_status`: 在线状态（仅私聊，需启用 `enable_online_status`），key为user_id，value为是否在线
- `is_hidden`: 会话是否被隐藏

#### 2. 获取消息历史

```http
GET /api/conversations/:id/messages?limit=50&offset=0
```

**响应**:
```json
{
  "messages": [
    {
      "id": "uuid",
      "conversation_id": "uuid",
      "sender_id": "uuid",
      "message_type": "text | image | emoji",
      "content": "Hello!",
      "metadata": {},
      "status": "sent | delivered | read",
      "is_recalled": false,
      "created_at": "2025-01-19T..."
    }
  ]
}
```

#### 3. 搜索消息

```http
GET /api/messages/search?q=关键词&conversation_id=uuid&limit=50&offset=0
Authorization: Bearer <token>
```

**请求参数**:
- `q` (必需): 搜索关键词
- `conversation_id` (可选): 指定会话ID，只搜索该会话；不指定则搜索所有会话
- `limit` (可选): 返回数量限制，默认50
- `offset` (可选): 偏移量，默认0

**响应**:
```json
{
  "success": true,
  "messages": [
    {
      "id": "uuid",
      "conversation_id": "uuid",
      "sender_id": "uuid",
      "message_type": "text",
      "content": "包含关键词的消息内容",
      "created_at": "2025-01-19T..."
    }
  ],
  "count": 10
}
```

**特性**:
- 不区分大小写搜索
- 只搜索文本类型消息（`message_type = "text"`）
- 自动排除已撤回的消息
- 权限验证：只能搜索自己有权限的会话
- 按时间倒序排列

#### 4. 隐藏会话（软删除）

```http
POST /api/conversations/:id/hide
Authorization: Bearer <token>
```

**功能**: 隐藏会话，隐藏后会话不出现在列表中，但收到新消息时会自动恢复显示

**响应**:
```json
{
  "success": true,
  "message": "conversation hidden successfully"
}
```

#### 4. 创建群聊

```http
POST /api/conversations/group
Content-Type: application/json

{
  "group_name": "项目讨论组",
  "member_ids": ["uuid1", "uuid2", "uuid3"]
}
```

---

### 群聊成员管理

#### 1. 添加成员

```http
POST /api/conversations/:id/members
Content-Type: application/json

{
  "member_ids": ["uuid1", "uuid2"]
}
```

**权限**: 需要 owner 或 admin 角色

#### 2. 移除成员

```http
POST /api/conversations/:id/members/remove
Content-Type: application/json

{
  "user_id": "uuid"
}
```

**权限**: 需要 owner 或 admin 角色
**限制**: 不能移除 owner

#### 3. 离开群聊

```http
POST /api/conversations/:id/leave
```

**限制**: owner 不能直接离开，需要先转让 owner 身份

#### 4. 更新成员角色

```http
POST /api/conversations/:id/members/:user_id/role
Content-Type: application/json

{
  "role": "owner | admin | member"
}
```

**权限**: 仅 owner 可操作

---

### 通知管理

#### 1. 获取通知列表

```http
GET /api/notifications?limit=50&offset=0&unread_only=false
```

**响应**:
```json
{
  "notifications": [
    {
      "id": "uuid",
      "user_id": "uuid",
      "notification_type": "system | message | card_completed | custom",
      "title": "新消息",
      "content": "你有一条新消息",
      "metadata": {
        "conversation_id": "uuid",
        "sender_name": "张三"
      },
      "is_read": false,
      "priority": 0,
      "created_at": "2025-01-19T...",
      "expires_at": null
    }
  ],
  "unread_count": 10,
  "latest_notif_time": "2025-01-19T10:30:00Z"
}
```

**新增字段说明**:
- `unread_count`: 未读通知数量（实时）
- `latest_notif_time`: 最新通知的时间（实时）

#### 2. 标记单个通知为已读

```http
POST /api/notifications/:id/read
```

#### 3. 标记所有通知为已读

```http
POST /api/notifications/read-all
```

#### 4. 删除通知

```http
POST /api/notifications/:id/delete
```

---

### 用户关系管理（拉黑）

#### 1. 拉黑用户

```http
POST /api/relationships/block
Content-Type: application/json

{
  "target_user_id": "uuid"
}
```

**效果**: 被拉黑的用户无法给你发送消息

#### 2. 取消拉黑

```http
POST /api/relationships/unblock
Content-Type: application/json

{
  "target_user_id": "uuid"
}
```

#### 3. 获取拉黑列表

```http
GET /api/relationships/blocked
```

**响应**:
```json
{
  "blocked_users": [
    {
      "id": "uuid",
      "user_id": "uuid",           // 你的 ID
      "target_user_id": "uuid",    // 被拉黑的用户 ID
      "relationship_type": "blocked",
      "created_at": "2025-01-19T..."
    }
  ]
}
```

---

### 管理员API（需要管理员权限）

#### 1. 获取系统设置

```http
GET /api/admin/settings
```

**响应**:
```json
{
  "settings": [
    {
      "key": "enable_first_message_limit",
      "value": "true",
      "description": "是否启用首条消息限制"
    },
    {
      "key": "max_video_size_mb",
      "value": "50",
      "description": "视频文件最大大小（MB）"
    }
  ]
}
```

#### 2. 更新系统设置

```http
POST /api/admin/settings/:key
Content-Type: application/json

{
  "value": "false"
}
```

**示例**:
```bash
# 关闭首条消息限制
POST /api/admin/settings/enable_first_message_limit
{"value": "false"}

# 修改视频大小限制
POST /api/admin/settings/max_video_size_mb
{"value": "100"}
```

#### 3. 重新加载系统设置

```http
POST /api/admin/settings/reload
```

**说明**: 从数据库重新加载所有系统设置到内存

#### 4. 获取通知模板列表

```http
GET /api/admin/notification-templates
```

**响应**:
```json
{
  "templates": [
    {
      "id": "uuid",
      "template_type": "message | system | card_completed",
      "title_template": "新消息",
      "content_template": "你有 {{count}} 条新消息",
      "is_default": true,
      "priority": 0,
      "created_at": "2025-01-19T..."
    }
  ]
}
```

#### 5. 创建通知模板

```http
POST /api/admin/notification-templates
Content-Type: application/json

{
  "template_type": "custom",
  "title_template": "系统公告",
  "content_template": "{{message}}",
  "priority": 1
}
```

#### 6. 更新通知模板

```http
POST /api/admin/notification-templates/:id
Content-Type: application/json

{
  "title_template": "更新的标题",
  "content_template": "更新的内容",
  "priority": 2
}
```

#### 7. 删除通知模板

```http
DELETE /api/admin/notification-templates/:id
```

#### 8. 初始化默认通知模板

```http
POST /api/admin/notification-templates/init-defaults
```

**说明**: 创建系统默认的通知模板（message, system, card_completed）

---

## 错误响应格式

所有错误响应格式统一：

```json
{
  "error": "错误描述信息"
}
```

常见 HTTP 状态码：
- `200 OK`: 成功
- `201 Created`: 创建成功
- `400 Bad Request`: 请求参数错误
- `401 Unauthorized`: 未认证
- `403 Forbidden`: 权限不足
- `404 Not Found`: 资源不存在
- `409 Conflict`: 冲突（如重复拉黑）
- `500 Internal Server Error`: 服务器错误

---

## 功能特性

### 1. 首条消息限制（防骚扰）

- 用户 A 首次给用户 B 发消息后，在 B 回复之前，A 不能再发送第二条消息
- 可通过会话设置关闭此功能：`enable_first_message_limit: false`

### 2. 拉黑功能

- 用户 A 拉黑 B 后，B 无法给 A 发送消息
- B 发送消息会收到 `"you are blocked by this user"` 错误

### 3. 消息撤回

- 仅能撤回 2 分钟内的消息
- 仅能撤回自己发送的消息
- 撤回后所有会话成员会收到 `recalled` 事件

### 4. 已读回执

- 可选功能，通过 `enable_read_receipt` 控制
- 发送者会收到其他成员的已读状态

### 5. 正在输入提示

- 可选功能，通过 `enable_typing_indicator` 控制
- 实时广播给会话中的其他在线成员

### 6. 离线消息

- 用户离线时，消息会存储到 Redis 队列
- 用户上线后可通过 HTTP API 获取历史消息

### 7. 未读计数

- 每个用户对每个会话都有独立的未读计数
- 标记已读后未读计数归零

---

## 完整流程示例

### 私聊完整流程

```javascript
// 1. 登录获取普通 JWT
const loginRes = await fetch('http://localhost:8080/auth/login', {
  method: 'POST',
  body: JSON.stringify({ email: 'user@example.com', password: '...' })
});
const { token } = await loginRes.json();

// 2. 获取 WebSocket Token
const wsTokenRes = await fetch('http://localhost:8080/message/ws-token', {
  headers: { 'Authorization': `Bearer ${token}` }
});
const { ws_token, ws_url } = await wsTokenRes.json();

// 3. 建立 WebSocket 连接
const ws = new WebSocket(`${ws_url}?token=${ws_token}`);

// 4. 发送心跳（每 30 秒）
setInterval(() => {
  ws.send(JSON.stringify({ type: 'heartbeat' }));
}, 30000);

// 5. 发送私聊消息（首次发送会自动创建会话）
ws.send(JSON.stringify({
  type: 'message',
  data: {
    receiver_id: 'target-user-uuid',
    message_type: 'text',
    content: 'Hello!'
  }
}));

// 6. 接收消息
ws.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  console.log('Received:', msg);

  if (msg.type === 'message') {
    // 显示新消息
    displayMessage(msg.data);

    // 发送已读回执
    ws.send(JSON.stringify({
      type: 'read',
      data: {
        conversation_id: msg.data.conversation_id,
        message_id: msg.data.id
      }
    }));
  }
};

// 7. 查询历史消息
const historyRes = await fetch(
  `http://localhost:8083/api/conversations/${conversationId}/messages?limit=50`,
  { headers: { 'Authorization': `Bearer ${token}` } }
);
const { messages } = await historyRes.json();
```

### 群聊完整流程

```javascript
// 1. 创建群聊
const groupRes = await fetch('http://localhost:8083/api/conversations/group', {
  method: 'POST',
  headers: {
    'Authorization': `Bearer ${token}`,
    'Content-Type': 'application/json'
  },
  body: JSON.stringify({
    group_name: '项目讨论组',
    member_ids: ['uuid1', 'uuid2', 'uuid3']
  })
});
const group = await groupRes.json();

// 2. 在 WebSocket 中发送群聊消息
ws.send(JSON.stringify({
  type: 'message',
  data: {
    conversation_id: group.id,
    message_type: 'text',
    content: 'Hello everyone!'
  }
}));

// 3. 添加新成员（需要 owner 或 admin 权限）
await fetch(`http://localhost:8083/api/conversations/${group.id}/members`, {
  method: 'POST',
  headers: {
    'Authorization': `Bearer ${token}`,
    'Content-Type': 'application/json'
  },
  body: JSON.stringify({
    member_ids: ['new-user-uuid']
  })
});

// 4. 离开群聊
await fetch(`http://localhost:8083/api/conversations/${group.id}/leave`, {
  method: 'POST',
  headers: { 'Authorization': `Bearer ${token}` }
});
```

---

## WebSocket 实时推送（v2.0新增）

### 1. 会话列表更新推送

**触发时机**: 当会话收到新消息时

**推送格式**:
```json
{
  "type": "conversation_update",
  "data": {
    "conversation_id": "uuid",
    "last_message_time": "2025-01-19T10:30:00Z",
    "last_message_text": "最新消息内容预览...",
    "unread_count": 5
  }
}
```

**作用**: 前端可以实时更新会话列表的最新消息时间和未读数量，无需重新请求接口

### 2. 通知更新推送

**触发时机**: 当用户收到新通知时

**推送格式**:
```json
{
  "type": "notification_update",
  "data": {
    "unread_count": 3,
    "latest_notif_time": "2025-01-19T10:30:00Z"
  }
}
```

**作用**: 前端可以实时更新通知小红点的未读数量

### 3. 未读数量更新推送（已有功能）

**触发时机**: 对方标记消息已读时（需启用 `enable_read_receipt`）

**推送格式**:
```json
{
  "type": "unread_count_update",
  "data": {
    "conversation_id": "uuid",
    "unread_count": 0
  }
}
```

---

## 性能建议

1. **WebSocket 心跳**: 每 30 秒发送一次，保持在线状态
2. **消息分页**: 历史消息建议每次加载 50 条
3. **通知轮询**: 如果不用 WebSocket，建议每 30-60 秒轮询一次通知接口
4. **已读优化**: 批量标记已读而非逐条标记

---

## 安全注意事项

1. **Token 过期**: WebSocket Token 5 分钟过期，需要重新获取
2. **拉黑检查**: 发送消息前会自动检查拉黑状态
3. **权限控制**: 群聊操作会严格检查用户权限
4. **防重放**: 考虑未来添加 Token 防重放机制

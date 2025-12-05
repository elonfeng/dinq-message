-- dinq_message 完整数据库初始化脚本
-- 包含：删表、建表、索引

-- ============================================
-- 删除现有表（如果存在）
-- ============================================
DROP TABLE IF EXISTS notification_templates CASCADE;
DROP TABLE IF EXISTS notifications CASCADE;
DROP TABLE IF EXISTS user_relationships CASCADE;
DROP TABLE IF EXISTS messages CASCADE;
DROP TABLE IF EXISTS conversation_members CASCADE;
DROP TABLE IF EXISTS conversations CASCADE;
DROP TABLE IF EXISTS system_settings CASCADE;

-- ============================================
-- 1. 会话表
-- ============================================
CREATE TABLE conversations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_type VARCHAR(20) NOT NULL,
    group_name VARCHAR(100),
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    last_message_at TIMESTAMP,
    last_message_id UUID  -- 冗余最新消息ID,用于高性能查询最新消息内容
);

CREATE INDEX idx_conv_type ON conversations(conversation_type);
CREATE INDEX idx_conv_last_message_at ON conversations(last_message_at DESC NULLS LAST);

-- ============================================
-- 2. 消息表
-- ============================================
CREATE TABLE messages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    sender_id UUID NOT NULL,
    message_type VARCHAR(20) NOT NULL,
    content TEXT,
    metadata JSONB,
    status VARCHAR(20) DEFAULT 'sent',
    reply_to_message_id UUID,
    is_recalled BOOLEAN DEFAULT FALSE,
    recalled_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX idx_msg_conversation ON messages(conversation_id, created_at DESC);
CREATE INDEX idx_msg_sender ON messages(sender_id, created_at DESC);
CREATE INDEX idx_msg_created ON messages(created_at DESC);
CREATE INDEX idx_msg_conversation_covering ON messages(conversation_id, created_at DESC) INCLUDE (sender_id, message_type, status, is_recalled);
CREATE INDEX idx_msg_recall_check ON messages(id, sender_id) INCLUDE (created_at, is_recalled);
CREATE INDEX idx_msg_search ON messages USING GIN (to_tsvector('simple', content)) WHERE message_type = 'text' AND is_recalled = FALSE;

-- ============================================
-- 3. 会话成员表
-- ============================================
CREATE TABLE conversation_members (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    user_id UUID NOT NULL,
    role VARCHAR(20) DEFAULT 'member',
    is_muted BOOLEAN DEFAULT FALSE,
    is_hidden BOOLEAN DEFAULT FALSE,
    joined_at TIMESTAMP DEFAULT NOW(),
    left_at TIMESTAMP,
    unread_count INT DEFAULT 0,
    last_read_message_id UUID,
    last_read_at TIMESTAMP,
    UNIQUE(conversation_id, user_id)
);

CREATE INDEX idx_member_user ON conversation_members(user_id);
CREATE INDEX idx_member_conv ON conversation_members(conversation_id);
CREATE INDEX idx_member_conv_user_active ON conversation_members(conversation_id, user_id) WHERE left_at IS NULL;
CREATE INDEX idx_member_user_active ON conversation_members(user_id, conversation_id) WHERE left_at IS NULL;
CREATE INDEX idx_member_user_not_hidden ON conversation_members(user_id, conversation_id) WHERE left_at IS NULL AND is_hidden = FALSE;

COMMENT ON COLUMN conversation_members.is_hidden IS '会话是否被用户隐藏(软删除),收到新消息时自动恢复显示';

-- ============================================
-- 4. 用户关系表
-- ============================================
CREATE TABLE user_relationships (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL,
    target_user_id UUID NOT NULL,
    relationship_type VARCHAR(20) NOT NULL,
    created_at TIMESTAMP DEFAULT NOW(),
    UNIQUE(user_id, target_user_id, relationship_type)
);

CREATE INDEX idx_relationship_user ON user_relationships(user_id, relationship_type);
CREATE INDEX idx_relationship_target ON user_relationships(target_user_id, user_id, relationship_type);

-- ============================================
-- 5. 系统配置表（超管全局配置）
-- ============================================
CREATE TABLE system_settings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    setting_key VARCHAR(100) NOT NULL UNIQUE,
    setting_value TEXT NOT NULL,
    description TEXT,
    updated_at TIMESTAMP DEFAULT NOW()
);

-- 插入默认系统配置
INSERT INTO system_settings (setting_key, setting_value, description) VALUES
    ('enable_typing_indicator', 'false', '启用正在输入提示功能(默认关闭)'),
    ('enable_read_receipt', 'true', '启用已读回执功能'),
    ('enable_online_status', 'true', '启用在线状态功能'),
    ('enable_first_message_limit', 'true', '启用首条消息限制功能'),
    ('enable_block_feature', 'false', '启用用户拉黑功能(默认关闭)'),
    ('max_video_size_mb', '100', '视频文件最大大小(MB)');

CREATE INDEX idx_system_settings_key ON system_settings(setting_key);

-- ============================================
-- 6. 通知模板表
-- ============================================
CREATE TABLE notification_templates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type VARCHAR(50) NOT NULL UNIQUE,
    title VARCHAR(200) NOT NULL,
    content_template TEXT,
    priority INTEGER DEFAULT 0,
    enable_push BOOLEAN DEFAULT true,
    enable_websocket BOOLEAN DEFAULT true,
    is_active BOOLEAN DEFAULT true,
    description TEXT,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX idx_notification_templates_type ON notification_templates(type);
CREATE INDEX idx_notification_templates_is_active ON notification_templates(is_active);

-- 插入默认模板（仅系统相关，消息通知已禁用）
INSERT INTO notification_templates (type, title, content_template, priority, enable_push, enable_websocket, is_active, description)
VALUES
    ('system', 'System Notification', '{{content}}', 1, true, true, true, '系统通知'),
    ('card_completed', 'Card Completed', 'Your card {{card_name}} is ready!', 0, true, true, true, '卡片生成完成通知');

-- ============================================
-- 7. 通知表
-- ============================================
CREATE TABLE notifications (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL,
    notification_type VARCHAR(50) NOT NULL,
    title VARCHAR(200) NOT NULL,
    content TEXT,
    metadata JSONB,
    is_read BOOLEAN DEFAULT FALSE,
    read_at TIMESTAMP,
    priority INT DEFAULT 0,
    created_at TIMESTAMP DEFAULT NOW(),
    expires_at TIMESTAMP
);

CREATE INDEX idx_notif_user ON notifications(user_id, created_at DESC);
CREATE INDEX idx_notif_unread ON notifications(user_id, is_read) WHERE is_read = FALSE;
CREATE INDEX idx_notif_expires ON notifications(expires_at) WHERE expires_at IS NOT NULL;

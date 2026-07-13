CREATE TABLE IF NOT EXISTS apps (
  id CHAR(36) PRIMARY KEY,
  name VARCHAR(128) NOT NULL,
  feishu_app_id VARCHAR(64) NOT NULL,
  feishu_app_secret VARCHAR(256) NOT NULL,
  workspace_dir VARCHAR(1024) NOT NULL,
  workspace_mode VARCHAR(16) NOT NULL DEFAULT 'work',
  model VARCHAR(128) NOT NULL,
  reasoning_effort VARCHAR(32) NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  UNIQUE KEY uk_apps_name (name),
  UNIQUE KEY uk_apps_feishu_app_id (feishu_app_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS chat_groups (
  id CHAR(36) PRIMARY KEY,
  app_id CHAR(36) NOT NULL,
  chat_type VARCHAR(16) NOT NULL,
  chat_id VARCHAR(128) NOT NULL,
  codex_thread_id VARCHAR(128) NULL,
  last_message_at DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  CONSTRAINT fk_chat_groups_app FOREIGN KEY (app_id) REFERENCES apps(id),
  UNIQUE KEY uk_chat_groups_app_chat (app_id, chat_type, chat_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS messages (
  id CHAR(36) PRIMARY KEY,
  trace_id CHAR(32) NOT NULL,
  chat_group_id CHAR(36) NOT NULL,
  feishu_event_id VARCHAR(128) NOT NULL,
  feishu_user_message_id VARCHAR(128) NOT NULL,
  feishu_bot_message_id VARCHAR(128) NULL,
  sender_open_id VARCHAR(128) NULL,
  user_content MEDIUMTEXT NOT NULL,
  assistant_content MEDIUMTEXT NULL,
  status VARCHAR(16) NOT NULL,
  error_code VARCHAR(64) NULL,
  duration_ms BIGINT UNSIGNED NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  completed_at DATETIME(3) NULL,
  CONSTRAINT fk_messages_chat_group FOREIGN KEY (chat_group_id) REFERENCES chat_groups(id),
  UNIQUE KEY uk_messages_trace_id (trace_id),
  UNIQUE KEY uk_messages_event (chat_group_id, feishu_event_id),
  UNIQUE KEY uk_messages_user_message (chat_group_id, feishu_user_message_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

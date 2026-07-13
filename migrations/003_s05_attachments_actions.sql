SET @s05_schema := DATABASE();

SET @s05_sql := (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE chat_groups ADD COLUMN codex_toolset_version VARCHAR(64) NULL AFTER codex_thread_id',
    'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = @s05_schema AND table_name = 'chat_groups' AND column_name = 'codex_toolset_version'
);
PREPARE s05_chat_groups_toolset FROM @s05_sql;
EXECUTE s05_chat_groups_toolset;
DEALLOCATE PREPARE s05_chat_groups_toolset;

CREATE TABLE IF NOT EXISTS attachments (
  id CHAR(36) PRIMARY KEY,
  message_id CHAR(36) NOT NULL,
  chat_group_id CHAR(36) NOT NULL,
  kind VARCHAR(16) NOT NULL,
  source_resource_ref_enc MEDIUMBLOB NULL,
  source_ref_key_version INT NULL,
  source_message_id VARCHAR(128) NOT NULL,
  original_name_safe VARCHAR(255) NULL,
  declared_mime VARCHAR(255) NULL,
  observed_mime VARCHAR(255) NULL,
  byte_size BIGINT UNSIGNED NULL,
  sha256 CHAR(64) NULL,
  session_id CHAR(36) NULL,
  relative_path VARCHAR(2048) CHARACTER SET ascii NULL,
  state VARCHAR(24) NOT NULL,
  error_code VARCHAR(64) NULL,
  attempt_id CHAR(36) NULL,
  lease_until DATETIME(3) NULL,
  retention_deadline DATETIME(3) NULL,
  deleted_at DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  CONSTRAINT fk_attachments_message FOREIGN KEY (message_id) REFERENCES messages(id),
  CONSTRAINT fk_attachments_chat_group FOREIGN KEY (chat_group_id) REFERENCES chat_groups(id),
  UNIQUE KEY uk_attachments_session_path (session_id, relative_path),
  KEY idx_attachments_cleanup (state, retention_deadline, lease_until),
  KEY idx_attachments_message (message_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS feishu_action_calls (
  id CHAR(36) PRIMARY KEY,
  app_id CHAR(36) NOT NULL,
  channel_key VARCHAR(320) NOT NULL,
  chat_group_id CHAR(36) NOT NULL,
  thread_id VARCHAR(128) NOT NULL,
  turn_id VARCHAR(128) NOT NULL,
  call_id VARCHAR(128) NOT NULL,
  tool VARCHAR(128) NOT NULL,
  arguments_digest CHAR(64) NOT NULL,
  state VARCHAR(32) NOT NULL,
  result_enc MEDIUMBLOB NULL,
  result_key_version INT NULL,
  object_id_digest CHAR(64) NULL,
  error_code VARCHAR(64) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  completed_at DATETIME(3) NULL,
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  CONSTRAINT fk_feishu_action_calls_app FOREIGN KEY (app_id) REFERENCES apps(id),
  CONSTRAINT fk_feishu_action_calls_chat_group FOREIGN KEY (chat_group_id) REFERENCES chat_groups(id),
  UNIQUE KEY uk_feishu_action_call (app_id, thread_id, turn_id, call_id),
  KEY idx_feishu_action_calls_state (state, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

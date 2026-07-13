SET @s06_schema := DATABASE();

SET @s06_sql := (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE messages ADD COLUMN received_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) AFTER user_content',
    'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = @s06_schema AND table_name = 'messages' AND column_name = 'received_at'
);
PREPARE s06_received_at FROM @s06_sql;
EXECUTE s06_received_at;
DEALLOCATE PREPARE s06_received_at;

SET @s06_sql := (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE messages ADD COLUMN command_kind VARCHAR(16) NULL AFTER received_at',
    'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = @s06_schema AND table_name = 'messages' AND column_name = 'command_kind'
);
PREPARE s06_command_kind FROM @s06_sql;
EXECUTE s06_command_kind;
DEALLOCATE PREPARE s06_command_kind;

SET @s06_sql := (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE messages ADD COLUMN command_payload_sha256 CHAR(64) NULL AFTER command_kind',
    'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = @s06_schema AND table_name = 'messages' AND column_name = 'command_payload_sha256'
);
PREPARE s06_command_digest FROM @s06_sql;
EXECUTE s06_command_digest;
DEALLOCATE PREPARE s06_command_digest;

SET @s06_sql := (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE messages ADD COLUMN command_payload_bytes INT UNSIGNED NULL AFTER command_payload_sha256',
    'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = @s06_schema AND table_name = 'messages' AND column_name = 'command_payload_bytes'
);
PREPARE s06_command_bytes FROM @s06_sql;
EXECUTE s06_command_bytes;
DEALLOCATE PREPARE s06_command_bytes;

SET @s06_sql := (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE messages ADD COLUMN command_effect_state VARCHAR(16) NULL AFTER command_payload_bytes',
    'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = @s06_schema AND table_name = 'messages' AND column_name = 'command_effect_state'
);
PREPARE s06_effect_state FROM @s06_sql;
EXECUTE s06_effect_state;
DEALLOCATE PREPARE s06_effect_state;

SET @s06_sql := (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE messages ADD COLUMN command_reply_outcome VARCHAR(16) NULL AFTER command_effect_state',
    'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = @s06_schema AND table_name = 'messages' AND column_name = 'command_reply_outcome'
);
PREPARE s06_reply_outcome FROM @s06_sql;
EXECUTE s06_reply_outcome;
DEALLOCATE PREPARE s06_reply_outcome;

SET @s06_sql := (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE messages ADD KEY idx_messages_command_effect (chat_group_id, command_kind, command_effect_state)',
    'SELECT 1')
  FROM information_schema.statistics
  WHERE table_schema = @s06_schema AND table_name = 'messages' AND index_name = 'idx_messages_command_effect'
);
PREPARE s06_command_index FROM @s06_sql;
EXECUTE s06_command_index;
DEALLOCATE PREPARE s06_command_index;

SET @s06_schedule_schema := DATABASE();

SET @s06_schedule_sql := (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE chat_groups ADD COLUMN codex_tool_catalog_version VARCHAR(64) NOT NULL DEFAULT ''s05-feishu-v2'' AFTER codex_toolset_version',
    'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = @s06_schedule_schema AND table_name = 'chat_groups' AND column_name = 'codex_tool_catalog_version'
);
PREPARE s06_schedule_catalog_version FROM @s06_schedule_sql;
EXECUTE s06_schedule_catalog_version;
DEALLOCATE PREPARE s06_schedule_catalog_version;

SET @s06_schedule_sql := (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE chat_groups ADD COLUMN catalog_upgrade_state ENUM(''stable'',''archive_pending'',''start_pending'') NOT NULL DEFAULT ''stable'' AFTER codex_tool_catalog_version',
    'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = @s06_schedule_schema AND table_name = 'chat_groups' AND column_name = 'catalog_upgrade_state'
);
PREPARE s06_schedule_catalog_state FROM @s06_schedule_sql;
EXECUTE s06_schedule_catalog_state;
DEALLOCATE PREPARE s06_schedule_catalog_state;

SET @s06_schedule_sql := (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE chat_groups ADD COLUMN catalog_upgrade_from_thread_id VARCHAR(128) NULL AFTER catalog_upgrade_state',
    'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = @s06_schedule_schema AND table_name = 'chat_groups' AND column_name = 'catalog_upgrade_from_thread_id'
);
PREPARE s06_schedule_catalog_from FROM @s06_schedule_sql;
EXECUTE s06_schedule_catalog_from;
DEALLOCATE PREPARE s06_schedule_catalog_from;

SET @s06_schedule_sql := (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE chat_groups ADD COLUMN catalog_upgrade_target VARCHAR(64) NULL AFTER catalog_upgrade_from_thread_id',
    'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = @s06_schedule_schema AND table_name = 'chat_groups' AND column_name = 'catalog_upgrade_target'
);
PREPARE s06_schedule_catalog_target FROM @s06_schedule_sql;
EXECUTE s06_schedule_catalog_target;
DEALLOCATE PREPARE s06_schedule_catalog_target;

SET @s06_schedule_sql := (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE chat_groups ADD COLUMN schedule_enabled BOOLEAN NOT NULL DEFAULT TRUE AFTER catalog_upgrade_target',
    'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = @s06_schedule_schema AND table_name = 'chat_groups' AND column_name = 'schedule_enabled'
);
PREPARE s06_schedule_enabled FROM @s06_schedule_sql;
EXECUTE s06_schedule_enabled;
DEALLOCATE PREPARE s06_schedule_enabled;

CREATE TABLE IF NOT EXISTS scheduled_tasks (
  id CHAR(36) PRIMARY KEY,
  app_id CHAR(36) NOT NULL,
  chat_group_id CHAR(36) NOT NULL,
  creator_open_id_hmac CHAR(64) NOT NULL,
  creator_open_id_enc MEDIUMBLOB NOT NULL,
  creator_key_version INT NOT NULL,
  kind ENUM('prompt','script') NOT NULL,
  cron_expression VARCHAR(128) NOT NULL,
  timezone VARCHAR(64) NOT NULL,
  payload_enc MEDIUMBLOB NOT NULL,
  payload_key_version INT NOT NULL,
  payload_hmac CHAR(64) NOT NULL,
  payload_bytes INT UNSIGNED NOT NULL,
  silent BOOLEAN NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  version BIGINT UNSIGNED NOT NULL,
  next_run_at DATETIME(3) NULL,
  last_run_at DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  CONSTRAINT fk_scheduled_tasks_app FOREIGN KEY (app_id) REFERENCES apps(id),
  CONSTRAINT fk_scheduled_tasks_chat_group FOREIGN KEY (chat_group_id) REFERENCES chat_groups(id),
  KEY idx_scheduled_tasks_due (enabled, next_run_at),
  KEY idx_scheduled_tasks_owner (app_id, chat_group_id, creator_open_id_hmac, updated_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS scheduled_script_definitions (
  id CHAR(36) PRIMARY KEY,
  app_id CHAR(36) NOT NULL,
  canonical_ref VARCHAR(1024) CHARACTER SET ascii NOT NULL,
  content_hmac CHAR(64) NOT NULL,
  key_version INT NOT NULL,
  revoked_at DATETIME(3) NULL,
  approved_at DATETIME(3) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  CONSTRAINT fk_scheduled_script_definitions_app FOREIGN KEY (app_id) REFERENCES apps(id),
  UNIQUE KEY uk_scheduled_script_definition (app_id, canonical_ref, content_hmac),
  KEY idx_scheduled_script_definitions_active (app_id, revoked_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS scheduled_task_runs (
  id CHAR(36) PRIMARY KEY,
  task_id CHAR(36) NOT NULL,
  scheduled_for DATETIME(3) NOT NULL,
  task_version BIGINT UNSIGNED NOT NULL,
  kind ENUM('prompt','script') NOT NULL,
  silent BOOLEAN NOT NULL,
  payload_enc MEDIUMBLOB NOT NULL,
  payload_key_version INT NOT NULL,
  payload_hmac CHAR(64) NOT NULL,
  payload_bytes INT UNSIGNED NOT NULL,
  script_definition_id CHAR(36) NULL,
  script_content_hmac CHAR(64) NULL,
  script_key_version INT NULL,
  state ENUM('claimed','queued','running','succeeded','failed','cancelled','unknown','skipped') NOT NULL,
  claim_token CHAR(36) NULL,
  lease_until DATETIME(3) NULL,
  started_at DATETIME(3) NULL,
  completed_at DATETIME(3) NULL,
  duration_ms BIGINT UNSIGNED NULL,
  error_code VARCHAR(64) NULL,
  thread_id VARCHAR(128) NULL,
  turn_id VARCHAR(128) NULL,
  synthetic_message_id CHAR(36) NULL,
  exit_code INT NULL,
  stdout_hmac CHAR(64) NULL,
  stderr_hmac CHAR(64) NULL,
  output_bytes BIGINT UNSIGNED NULL,
  output_truncated BOOLEAN NOT NULL DEFAULT FALSE,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  CONSTRAINT fk_scheduled_task_runs_task FOREIGN KEY (task_id) REFERENCES scheduled_tasks(id),
  CONSTRAINT fk_scheduled_task_runs_script FOREIGN KEY (script_definition_id) REFERENCES scheduled_script_definitions(id),
  UNIQUE KEY uk_scheduled_task_run_slot (task_id, scheduled_for),
  KEY idx_scheduled_task_runs_reconcile (state, lease_until),
  KEY idx_scheduled_task_runs_task (task_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS scheduled_task_deliveries (
  id CHAR(36) PRIMARY KEY,
  run_id CHAR(36) NOT NULL,
  delivery_kind ENUM('result_card','fallback_text') NOT NULL,
  attempt SMALLINT UNSIGNED NOT NULL,
  stage ENUM('pending','in_flight') NULL,
  outcome ENUM('sent','rejected','unknown','suppressed') NULL,
  feishu_bot_message_id_hmac CHAR(64) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  completed_at DATETIME(3) NULL,
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  CONSTRAINT fk_scheduled_task_deliveries_run FOREIGN KEY (run_id) REFERENCES scheduled_task_runs(id),
  UNIQUE KEY uk_scheduled_task_delivery (run_id, delivery_kind, attempt),
  KEY idx_scheduled_task_deliveries_reconcile (stage, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS scheduled_task_tool_calls (
  id CHAR(36) PRIMARY KEY,
  app_id CHAR(36) NOT NULL,
  channel_key VARCHAR(320) NOT NULL,
  chat_group_id CHAR(36) NOT NULL,
  thread_id VARCHAR(128) NOT NULL,
  turn_id VARCHAR(128) NOT NULL,
  call_id VARCHAR(128) NOT NULL,
  tool VARCHAR(128) NOT NULL,
  arguments_hmac CHAR(64) NOT NULL,
  state ENUM('claimed','in_flight','succeeded','rejected') NOT NULL,
  result_enc MEDIUMBLOB NULL,
  result_key_version INT NULL,
  lease_until DATETIME(3) NULL,
  error_code VARCHAR(64) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  completed_at DATETIME(3) NULL,
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  CONSTRAINT fk_scheduled_task_tool_calls_app FOREIGN KEY (app_id) REFERENCES apps(id),
  CONSTRAINT fk_scheduled_task_tool_calls_chat_group FOREIGN KEY (chat_group_id) REFERENCES chat_groups(id),
  UNIQUE KEY uk_scheduled_task_tool_call (app_id, thread_id, turn_id, call_id),
  KEY idx_scheduled_task_tool_calls_reconcile (state, lease_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

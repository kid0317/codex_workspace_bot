CREATE TABLE IF NOT EXISTS thread_usage_snapshots (
  app_id CHAR(36) NOT NULL,
  chat_type VARCHAR(16) NOT NULL,
  chat_id VARCHAR(128) NOT NULL,
  thread_id VARCHAR(128) NOT NULL,
  last_trace_id CHAR(32) NULL,
  last_turn_id VARCHAR(128) NULL,
  input_tokens BIGINT UNSIGNED NOT NULL,
  output_tokens BIGINT UNSIGNED NOT NULL,
  cached_input_tokens BIGINT UNSIGNED NOT NULL,
  reasoning_output_tokens BIGINT UNSIGNED NOT NULL,
  total_tokens BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (app_id, chat_type, chat_id, thread_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

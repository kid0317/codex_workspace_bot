CREATE TABLE IF NOT EXISTS turn_usage_ledger (
  trace_id CHAR(32) NOT NULL,
  thread_id VARCHAR(128) NULL,
  turn_id VARCHAR(128) NOT NULL,
  input_tokens BIGINT UNSIGNED NOT NULL,
  output_tokens BIGINT UNSIGNED NOT NULL,
  cached_input_tokens BIGINT UNSIGNED NOT NULL,
  reasoning_output_tokens BIGINT UNSIGNED NOT NULL,
  total_tokens BIGINT UNSIGNED NOT NULL,
  recorded_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (trace_id, turn_id),
  KEY idx_turn_usage_ledger_turn (turn_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS session_usage_totals (
  app_id CHAR(36) NOT NULL,
  chat_type VARCHAR(16) NOT NULL,
  chat_id VARCHAR(128) NOT NULL,
  input_tokens BIGINT UNSIGNED NOT NULL DEFAULT 0,
  output_tokens BIGINT UNSIGNED NOT NULL DEFAULT 0,
  cached_input_tokens BIGINT UNSIGNED NOT NULL DEFAULT 0,
  reasoning_output_tokens BIGINT UNSIGNED NOT NULL DEFAULT 0,
  total_tokens BIGINT UNSIGNED NOT NULL DEFAULT 0,
  completed_turn_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  last_trace_id CHAR(32) NULL,
  last_thread_id VARCHAR(128) NULL,
  last_turn_id VARCHAR(128) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (app_id, chat_type, chat_id),
  CONSTRAINT fk_session_usage_totals_app FOREIGN KEY (app_id) REFERENCES apps(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

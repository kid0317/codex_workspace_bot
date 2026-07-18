-- This is a personal-local deployment. Scheduled task inputs are intentionally
-- inspectable in MySQL; credentials and log redaction remain outside this
-- decision. Legacy encrypted columns stay only long enough for the startup
-- data migration to read and clear them.
ALTER TABLE scheduled_tasks
  ADD COLUMN creator_open_id VARCHAR(255) NULL AFTER creator_open_id_hmac,
  ADD COLUMN payload_text MEDIUMTEXT NULL AFTER timezone,
  MODIFY creator_open_id_enc MEDIUMBLOB NULL,
  MODIFY creator_key_version INT NULL,
  MODIFY payload_enc MEDIUMBLOB NULL,
  MODIFY payload_key_version INT NULL,
  MODIFY payload_hmac CHAR(64) NULL,
  MODIFY payload_bytes INT UNSIGNED NULL,
  ADD KEY idx_scheduled_tasks_owner_plain (app_id, chat_group_id, creator_open_id, updated_at, id);

ALTER TABLE scheduled_task_runs
  ADD COLUMN payload_text MEDIUMTEXT NULL AFTER silent,
  MODIFY payload_enc MEDIUMBLOB NULL,
  MODIFY payload_key_version INT NULL,
  MODIFY payload_hmac CHAR(64) NULL,
  MODIFY payload_bytes INT UNSIGNED NULL;

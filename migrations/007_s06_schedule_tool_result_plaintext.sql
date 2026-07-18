ALTER TABLE scheduled_task_tool_calls
  ADD COLUMN result_text MEDIUMTEXT NULL AFTER result_key_version,
  MODIFY result_enc MEDIUMBLOB NULL,
  MODIFY result_key_version INT NULL;

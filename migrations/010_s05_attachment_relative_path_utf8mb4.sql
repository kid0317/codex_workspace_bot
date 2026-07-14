SET @s10_schema := DATABASE();

SET @s10_sql := (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE attachments DROP INDEX uk_attachments_session_path, MODIFY COLUMN relative_path VARCHAR(2048) CHARACTER SET utf8mb4 NULL, ADD UNIQUE KEY uk_attachments_session_path (session_id, relative_path(700))',
    'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = @s10_schema AND table_name = 'attachments' AND column_name = 'relative_path'
    AND character_set_name = 'utf8mb4'
);
PREPARE s10_attachment_relative_path FROM @s10_sql;
EXECUTE s10_attachment_relative_path;
DEALLOCATE PREPARE s10_attachment_relative_path;

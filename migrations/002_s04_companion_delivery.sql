SET @s04_schema := DATABASE();

SET @s04_sql := (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE messages ADD COLUMN companion_delivery_batch_id CHAR(36) NULL AFTER error_code',
    'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = @s04_schema AND table_name = 'messages' AND column_name = 'companion_delivery_batch_id'
);
PREPARE s04_column_batch FROM @s04_sql;
EXECUTE s04_column_batch;
DEALLOCATE PREPARE s04_column_batch;

SET @s04_sql := (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE messages ADD COLUMN delivery_stage VARCHAR(40) NULL AFTER companion_delivery_batch_id',
    'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = @s04_schema AND table_name = 'messages' AND column_name = 'delivery_stage'
);
PREPARE s04_column_stage FROM @s04_sql;
EXECUTE s04_column_stage;
DEALLOCATE PREPARE s04_column_stage;

SET @s04_sql := (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE messages ADD KEY idx_messages_delivery_reconcile (status, delivery_stage, companion_delivery_batch_id)',
    'SELECT 1')
  FROM information_schema.statistics
  WHERE table_schema = @s04_schema AND table_name = 'messages' AND index_name = 'idx_messages_delivery_reconcile'
);
PREPARE s04_index FROM @s04_sql;
EXECUTE s04_index;
DEALLOCATE PREPARE s04_index;

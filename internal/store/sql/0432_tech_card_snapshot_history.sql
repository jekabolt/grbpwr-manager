-- +migrate Up

-- Autosave history metadata. Every ALTER is guarded independently because MySQL DDL commits each
-- statement: a retry after a partial deploy must finish the migration rather than fail on the first
-- column that already exists. All columns are additive and nullable except the safe boolean default.

SET @snapshot_save_session_col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_snapshot' AND COLUMN_NAME = 'save_session_id');
SET @ddl := IF(@snapshot_save_session_col_exists = 0,
    'ALTER TABLE tech_card_snapshot ADD COLUMN save_session_id VARCHAR(36) NULL AFTER created_by',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @snapshot_bucket_start_col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_snapshot' AND COLUMN_NAME = 'bucket_start');
SET @ddl := IF(@snapshot_bucket_start_col_exists = 0,
    'ALTER TABLE tech_card_snapshot ADD COLUMN bucket_start DATETIME NULL AFTER save_session_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @snapshot_changed_paths_col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_snapshot' AND COLUMN_NAME = 'changed_paths');
SET @ddl := IF(@snapshot_changed_paths_col_exists = 0,
    'ALTER TABLE tech_card_snapshot ADD COLUMN changed_paths JSON NULL AFTER bucket_start',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @snapshot_pinned_col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_snapshot' AND COLUMN_NAME = 'pinned');
SET @ddl := IF(@snapshot_pinned_col_exists = 0,
    'ALTER TABLE tech_card_snapshot ADD COLUMN pinned BOOL NOT NULL DEFAULT FALSE AFTER changed_paths',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 4 + 36*4 + 8 bytes at utf8mb4 is far below InnoDB's 3072-byte composite-key ceiling.
SET @snapshot_session_bucket_idx_exists := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_snapshot'
      AND INDEX_NAME = 'idx_tc_snapshot_session_bucket');
SET @ddl := IF(@snapshot_session_bucket_idx_exists = 0,
    'CREATE INDEX idx_tc_snapshot_session_bucket ON tech_card_snapshot (tech_card_id, save_session_id, bucket_start)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @snapshot_session_bucket_idx_exists := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_snapshot'
      AND INDEX_NAME = 'idx_tc_snapshot_session_bucket');
SET @ddl := IF(@snapshot_session_bucket_idx_exists > 0,
    'DROP INDEX idx_tc_snapshot_session_bucket ON tech_card_snapshot',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @snapshot_pinned_col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_snapshot' AND COLUMN_NAME = 'pinned');
SET @ddl := IF(@snapshot_pinned_col_exists = 1,
    'ALTER TABLE tech_card_snapshot DROP COLUMN pinned',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @snapshot_changed_paths_col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_snapshot' AND COLUMN_NAME = 'changed_paths');
SET @ddl := IF(@snapshot_changed_paths_col_exists = 1,
    'ALTER TABLE tech_card_snapshot DROP COLUMN changed_paths',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @snapshot_bucket_start_col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_snapshot' AND COLUMN_NAME = 'bucket_start');
SET @ddl := IF(@snapshot_bucket_start_col_exists = 1,
    'ALTER TABLE tech_card_snapshot DROP COLUMN bucket_start',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @snapshot_save_session_col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_snapshot' AND COLUMN_NAME = 'save_session_id');
SET @ddl := IF(@snapshot_save_session_col_exists = 1,
    'ALTER TABLE tech_card_snapshot DROP COLUMN save_session_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

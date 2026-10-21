-- +migrate Up

-- A history row of a replaced or removed pattern sheet names the tech_card_snapshot that the SAME
-- UpdateTechCard transaction froze (the card as it was while that sheet was current), so «restore
-- v.N» can restore the whole card, not just re-attach the file. NULL = no snapshot was taken (an
-- ordinary save without snapshot_reason, or a row archived before this column) — not restorable.
--
-- No FK, on purpose. Both tables are append-only and both cascade from tech_card, so the only way a
-- snapshot row disappears is the deletion of the card, which removes these history rows in the same
-- statement; an FK would guard nothing and would cost a table rebuild of a populated table.
-- No CHECK: every existing row is legitimately NULL.
-- Column and index are guarded separately so a rerun repairs a partial DDL application.

SET @history_snapshot_col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_size_pattern_history' AND COLUMN_NAME = 'snapshot_id');
SET @ddl := IF(@history_snapshot_col_exists = 0,
    'ALTER TABLE tech_card_size_pattern_history ADD COLUMN snapshot_id BIGINT NULL COMMENT ''tech_card_snapshot.id taken by the save that archived this row; NULL = no snapshot'' AFTER reason',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @history_snapshot_idx_exists := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_size_pattern_history'
      AND INDEX_NAME = 'idx_tc_pattern_history_snapshot');
SET @ddl := IF(@history_snapshot_idx_exists = 0,
    'CREATE INDEX idx_tc_pattern_history_snapshot ON tech_card_size_pattern_history (snapshot_id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @history_snapshot_idx_exists := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_size_pattern_history'
      AND INDEX_NAME = 'idx_tc_pattern_history_snapshot');
SET @ddl := IF(@history_snapshot_idx_exists > 0,
    'DROP INDEX idx_tc_pattern_history_snapshot ON tech_card_size_pattern_history',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @history_snapshot_col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_size_pattern_history' AND COLUMN_NAME = 'snapshot_id');
SET @ddl := IF(@history_snapshot_col_exists = 1,
    'ALTER TABLE tech_card_size_pattern_history DROP COLUMN snapshot_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Up

-- An edited automatic FUSING operation survives when its INTERLINING line disappears. Its live
-- operation->BOM FK must be removed, but the automatic group still needs a durable identity:
-- otherwise the linkless orphan is indistinguishable from the shared unresolved group and a later
-- reconcile adopts it. This snapshot is private write metadata, not another live BOM reference.

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'auto_fusing_line_key');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_operation ADD COLUMN auto_fusing_line_key CHAR(26) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL COMMENT ''private automatic FUSING group snapshot; empty = unresolved, non-empty = original INTERLINING line_key'' AFTER review',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- No backfill: rows older than this column stay NULL (manual), so reconcile never adopts or
-- mutates a pre-existing FUSING step.

-- +migrate Down

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'auto_fusing_line_key');
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card_operation DROP COLUMN auto_fusing_line_key',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

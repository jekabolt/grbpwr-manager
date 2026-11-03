-- A tech-card step stores the order the owner set for its inputs (TechCardOperation.layer_order,
-- proto field 75): input keys, bottom to top, as a JSON array.
--
-- Until now a step's stacking lived in flags on its seam and pose rows, so two blocks with no row
-- between them had nowhere to keep it. NULL = no order set, and the step reads its rows as before.
--
-- A COLUMN, NOT A CHILD TABLE: it is one value of the step, it has no reference to anything (a stale
-- key is skipped when read, a new input lies on top), and operations are full-replaced on every card
-- save, so it is rewritten with its step in the same transaction.
--
-- INERT FOR THE OLD BINARY: it selects and inserts by named columns, so it neither reads this one nor
-- fails on it. No backfill. Safe under ANSI_QUOTES (single-quoted literals only). Guarded because
-- MySQL has no ADD COLUMN IF NOT EXISTS and DDL auto-commits before the migration row.

-- +migrate Up

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'layer_order');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_operation ADD COLUMN layer_order JSON NULL COMMENT ''input keys of this step bottom to top, NULL when no order is set'' AFTER assembly_block_detached',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'layer_order');
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card_operation DROP COLUMN layer_order',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

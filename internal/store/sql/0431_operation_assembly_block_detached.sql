-- +migrate Up

-- A piece-processing operation may sit between two JOIN blocks without belonging to either one.
-- Existing rows remain attached by the derived legacy behaviour. The additive boolean is guarded
-- because MySQL has no ADD COLUMN IF NOT EXISTS and DDL auto-commits before the migration row.

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'assembly_block_detached');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_operation ADD COLUMN assembly_block_detached TINYINT(1) NOT NULL DEFAULT 0 COMMENT ''piece-processing operation is outside every assembly block'' AFTER output_unit_name',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'assembly_block_detached');
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card_operation DROP COLUMN assembly_block_detached',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

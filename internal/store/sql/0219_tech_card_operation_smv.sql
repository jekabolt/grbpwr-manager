-- +migrate Up
-- Standard minute value for a sewing operation. NULL keeps existing operations and older clients
-- fully compatible. The column and named CHECK are guarded separately so a retry after any partial
-- DDL application can finish safely.

SET @smv_col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'smv'
);
SET @ddl := IF(@smv_col_exists = 0,
    'ALTER TABLE tech_card_operation ADD COLUMN smv DECIMAL(7,3) NULL COMMENT ''standard minute value; NULL = unset''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @smv_chk_exists := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_operation'
      AND CONSTRAINT_NAME = 'chk_tech_card_operation_smv'
);
SET @ddl := IF(@smv_chk_exists = 0,
    'ALTER TABLE tech_card_operation ADD CONSTRAINT chk_tech_card_operation_smv CHECK (smv IS NULL OR smv >= 0)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
SET @smv_chk_exists := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_operation'
      AND CONSTRAINT_NAME = 'chk_tech_card_operation_smv'
);
SET @ddl := IF(@smv_chk_exists = 1,
    'ALTER TABLE tech_card_operation DROP CHECK chk_tech_card_operation_smv',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @smv_col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'smv'
);
SET @ddl := IF(@smv_col_exists = 1,
    'ALTER TABLE tech_card_operation DROP COLUMN smv',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

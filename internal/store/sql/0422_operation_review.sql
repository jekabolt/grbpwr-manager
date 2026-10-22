-- +migrate Up

-- DXF reconciliation leaves a closed review reason on an operation until a person clears it.
-- Existing rows receive NULL, so the named CHECK is a retroactively true invariant.
-- Column and constraint are guarded separately so a rerun repairs a partial DDL application.

SET @review_col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation' AND COLUMN_NAME = 'review');
SET @ddl := IF(@review_col_exists = 0,
    'ALTER TABLE tech_card_operation ADD COLUMN review VARCHAR(16) NULL COMMENT ''DXF reconciliation review reason; NULL means reviewed'' AFTER draft',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @review_check_exists := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND CONSTRAINT_NAME = 'chk_tech_card_operation_review' AND CONSTRAINT_TYPE = 'CHECK');
SET @ddl := IF(@review_check_exists = 0,
    'ALTER TABLE tech_card_operation ADD CONSTRAINT chk_tech_card_operation_review CHECK (review IS NULL OR review IN (''reshaped'', ''remapped'', ''split'', ''orphan''))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @review_check_exists := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND CONSTRAINT_NAME = 'chk_tech_card_operation_review' AND CONSTRAINT_TYPE = 'CHECK');
SET @ddl := IF(@review_check_exists = 1,
    'ALTER TABLE tech_card_operation DROP CHECK chk_tech_card_operation_review',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @review_col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation' AND COLUMN_NAME = 'review');
SET @ddl := IF(@review_col_exists = 1,
    'ALTER TABLE tech_card_operation DROP COLUMN review',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Up

-- A purchase receipt may point at the supplier invoice already held in the private files library.
-- The three DDL steps are separate and independently guarded because MySQL auto-commits each one.
-- Existing rows start NULL. ADD COLUMN is metadata-only (INSTANT); the secondary index and foreign
-- key are built INPLACE. The FK checks can be disabled safely for its build because every pre-existing
-- value is NULL and no application writer exists before automigrate completes.

SET @msm_invoice_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement'
      AND COLUMN_NAME = 'invoice_file_id');
SET @ddl := IF(@msm_invoice_col = 0,
    'ALTER TABLE material_stock_movement
        ADD COLUMN invoice_file_id INT NULL,
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @msm_invoice_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement'
      AND INDEX_NAME = 'idx_msm_invoice_file');
SET @ddl := IF(@msm_invoice_idx = 0,
    'ALTER TABLE material_stock_movement
        ADD INDEX idx_msm_invoice_file (invoice_file_id),
        ALGORITHM=INPLACE, LOCK=NONE',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @msm_invoice_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement'
      AND CONSTRAINT_NAME = 'fk_msm_invoice_file' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @msm_old_fk_checks := @@SESSION.FOREIGN_KEY_CHECKS;
SET FOREIGN_KEY_CHECKS = 0;
SET @ddl := IF(@msm_invoice_fk = 0,
    'ALTER TABLE material_stock_movement
        ADD CONSTRAINT fk_msm_invoice_file FOREIGN KEY (invoice_file_id)
            REFERENCES library_file(id) ON DELETE SET NULL,
        ALGORITHM=INPLACE, LOCK=NONE',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
SET FOREIGN_KEY_CHECKS = @msm_old_fk_checks;

-- +migrate Down

SET @msm_invoice_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement'
      AND CONSTRAINT_NAME = 'fk_msm_invoice_file' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@msm_invoice_fk = 1,
    'ALTER TABLE material_stock_movement DROP FOREIGN KEY fk_msm_invoice_file,
        ALGORITHM=INPLACE, LOCK=NONE',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @msm_invoice_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement'
      AND INDEX_NAME = 'idx_msm_invoice_file');
SET @ddl := IF(@msm_invoice_idx = 1,
    'ALTER TABLE material_stock_movement DROP INDEX idx_msm_invoice_file,
        ALGORITHM=INPLACE, LOCK=NONE',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @msm_invoice_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement'
      AND COLUMN_NAME = 'invoice_file_id');
SET @ddl := IF(@msm_invoice_col = 1,
    'ALTER TABLE material_stock_movement DROP COLUMN invoice_file_id,
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

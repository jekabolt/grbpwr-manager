-- +migrate Up

-- Material-to-material conversion and physical lot shrinkage. Conversion moves value through WIP;
-- shrinkage changes quantity and average cost without changing material value.
--
-- Every ALTER is independently guarded because MySQL DDL auto-commits. A process killed between
-- two statements can safely run this whole file again.
-- migrationlint:allow-retroactive nullable/defaulted columns are history-safe; replacement CHECKs widen closed sets, and new FKs reference already-valid nullable ids

CREATE TABLE IF NOT EXISTS material_conversion (
    id INT AUTO_INCREMENT PRIMARY KEY,
    output_material_id INT NOT NULL,
    supplier_id INT NULL,
    work_cost DECIMAL(12,2) NOT NULL DEFAULT 0,
    work_currency VARCHAR(4) NULL,
    work_cost_base DECIMAL(12,2) NOT NULL DEFAULT 0,
    inputs_cost_base DECIMAL(16,6) NOT NULL DEFAULT 0,
    inputs_value_base DECIMAL(12,2) NOT NULL DEFAULT 0,
    total_cost_base DECIMAL(16,6) NOT NULL DEFAULT 0,
    output_unit_cost_base DECIMAL(12,4) NOT NULL DEFAULT 0,
    output_avg_before DECIMAL(12,4) NULL,
    supplier_doc VARCHAR(255) NULL,
    comment VARCHAR(255) NULL,
    client_request_id VARCHAR(64) NULL,
    admin_username VARCHAR(255) NOT NULL DEFAULT '',
    occurred_at DATE NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_mconv_output_material FOREIGN KEY (output_material_id) REFERENCES material(id),
    CONSTRAINT fk_mconv_supplier FOREIGN KEY (supplier_id) REFERENCES supplier(id) ON DELETE SET NULL,
    CONSTRAINT uniq_mconv_request UNIQUE (client_request_id),
    CONSTRAINT chk_mconv_work CHECK (work_cost >= 0 AND work_cost_base >= 0),
    INDEX idx_mconv_output_material (output_material_id),
    INDEX idx_mconv_supplier (supplier_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND COLUMN_NAME = 'conversion_id');
SET @ddl := IF(@need_col,
    'ALTER TABLE material_stock_movement ADD COLUMN conversion_id INT NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND COLUMN_NAME = 'loss_qty');
SET @ddl := IF(@need_col,
    'ALTER TABLE material_stock_movement ADD COLUMN loss_qty DECIMAL(12,3) NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @need_idx := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND INDEX_NAME = 'idx_msm_conversion');
SET @ddl := IF(@need_idx,
    'ALTER TABLE material_stock_movement ADD INDEX idx_msm_conversion (conversion_id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND CONSTRAINT_NAME = 'fk_msm_conversion');
SET @ddl := IF(@need_fk,
    'ALTER TABLE material_stock_movement ADD CONSTRAINT fk_msm_conversion FOREIGN KEY (conversion_id) REFERENCES material_conversion(id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND CONSTRAINT_NAME = 'chk_msm_loss');
SET @ddl := IF(@need_chk,
    'ALTER TABLE material_stock_movement ADD CONSTRAINT chk_msm_loss CHECK (loss_qty IS NULL OR (loss_qty >= 0 AND loss_qty <= quantity))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Replace the movement type CHECK only when it is still the old, narrower definition. If a crash
-- happens after the DROP, the retry skips the DROP and performs the missing ADD.
SET @old_chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'material_stock_movement'
      AND tc.CONSTRAINT_NAME = 'chk_msm_type' AND cc.CHECK_CLAUSE NOT LIKE '%convert_in%');
SET @ddl := IF(@old_chk = 1,
    'ALTER TABLE material_stock_movement DROP CHECK chk_msm_type',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND CONSTRAINT_NAME = 'chk_msm_type');
SET @ddl := IF(@need_chk,
    'ALTER TABLE material_stock_movement ADD CONSTRAINT chk_msm_type CHECK (movement_type REGEXP ''^(receipt|receipt_production|issue_production|issue_sample|return_production|return_sample|adjustment|writeoff|convert_out|convert_in|shrinkage)$'')',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Conversion is a truthful price-history provenance, not a production-run receipt.
SET @old_chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'material_price'
      AND tc.CONSTRAINT_NAME = 'chk_material_price_source' AND cc.CHECK_CLAUSE NOT LIKE '%conversion%');
SET @ddl := IF(@old_chk = 1,
    'ALTER TABLE material_price DROP CHECK chk_material_price_source',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_price' AND CONSTRAINT_NAME = 'chk_material_price_source');
SET @ddl := IF(@need_chk,
    'ALTER TABLE material_price ADD CONSTRAINT chk_material_price_source CHECK (source REGEXP ''^(manual|production_run|purchase|conversion)$'')',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND COLUMN_NAME = 'source_material_id');
SET @ddl := IF(@need_col,
    'ALTER TABLE material ADD COLUMN source_material_id INT NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @need_idx := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND INDEX_NAME = 'idx_material_source_material');
SET @ddl := IF(@need_idx,
    'ALTER TABLE material ADD INDEX idx_material_source_material (source_material_id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND CONSTRAINT_NAME = 'fk_material_source_material');
SET @ddl := IF(@need_fk,
    'ALTER TABLE material ADD CONSTRAINT fk_material_source_material FOREIGN KEY (source_material_id) REFERENCES material(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot' AND COLUMN_NAME = 'shrunk_qty');
SET @ddl := IF(@need_col,
    'ALTER TABLE material_lot ADD COLUMN shrunk_qty DECIMAL(12,3) NOT NULL DEFAULT 0',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot' AND COLUMN_NAME = 'washed_width_cm');
SET @ddl := IF(@need_col,
    'ALTER TABLE material_lot ADD COLUMN washed_width_cm DECIMAL(6,2) NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot' AND COLUMN_NAME = 'shrinkage_pct');
SET @ddl := IF(@need_col,
    'ALTER TABLE material_lot ADD COLUMN shrinkage_pct DECIMAL(5,2) NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot' AND CONSTRAINT_NAME = 'chk_material_lot_shrunk');
SET @ddl := IF(@need_chk,
    'ALTER TABLE material_lot ADD CONSTRAINT chk_material_lot_shrunk CHECK (shrunk_qty >= 0 AND shrunk_qty <= received_qty)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot' AND CONSTRAINT_NAME = 'chk_material_lot_washed_width');
SET @ddl := IF(@need_chk,
    'ALTER TABLE material_lot ADD CONSTRAINT chk_material_lot_washed_width CHECK (washed_width_cm IS NULL OR washed_width_cm > 0)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

CREATE TABLE IF NOT EXISTS material_lot_shrinkage (
    id INT AUTO_INCREMENT PRIMARY KEY,
    movement_id INT NOT NULL,
    lot_id INT NOT NULL,
    material_id INT NOT NULL,
    length_before DECIMAL(12,3) NOT NULL,
    length_after DECIMAL(12,3) NOT NULL,
    width_before DECIMAL(6,2) NULL,
    width_after DECIMAL(6,2) NULL,
    avg_before DECIMAL(12,4) NULL,
    avg_after DECIMAL(12,4) NULL,
    lot_pct_after DECIMAL(5,2) NOT NULL,
    material_pct_before DECIMAL(5,2) NULL,
    material_pct_after DECIMAL(5,2) NULL,
    admin_username VARCHAR(255) NOT NULL DEFAULT '',
    comment VARCHAR(255) NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uniq_mlsh_movement UNIQUE (movement_id),
    CONSTRAINT fk_mlsh_movement FOREIGN KEY (movement_id) REFERENCES material_stock_movement(id),
    CONSTRAINT fk_mlsh_lot FOREIGN KEY (lot_id) REFERENCES material_lot(id),
    CONSTRAINT fk_mlsh_material FOREIGN KEY (material_id) REFERENCES material(id),
    INDEX idx_mlsh_lot (lot_id),
    INDEX idx_mlsh_material (material_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down

SET @new_rows := (SELECT COUNT(*) FROM material_stock_movement
    WHERE movement_type IN ('convert_out','convert_in','shrinkage'));

SET @ddl := IF(@new_rows = 0,
    'DROP TABLE IF EXISTS material_lot_shrinkage',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot' AND CONSTRAINT_NAME = 'chk_material_lot_washed_width');
SET @ddl := IF(@has_chk = 1 AND @new_rows = 0,
    'ALTER TABLE material_lot DROP CHECK chk_material_lot_washed_width', 'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot' AND CONSTRAINT_NAME = 'chk_material_lot_shrunk');
SET @ddl := IF(@has_chk = 1 AND @new_rows = 0,
    'ALTER TABLE material_lot DROP CHECK chk_material_lot_shrunk', 'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot' AND COLUMN_NAME = 'shrinkage_pct');
SET @ddl := IF(@has_col = 1 AND @new_rows = 0,
    'ALTER TABLE material_lot DROP COLUMN shrinkage_pct', 'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot' AND COLUMN_NAME = 'washed_width_cm');
SET @ddl := IF(@has_col = 1 AND @new_rows = 0,
    'ALTER TABLE material_lot DROP COLUMN washed_width_cm', 'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot' AND COLUMN_NAME = 'shrunk_qty');
SET @ddl := IF(@has_col = 1 AND @new_rows = 0,
    'ALTER TABLE material_lot DROP COLUMN shrunk_qty', 'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND CONSTRAINT_NAME = 'fk_material_source_material');
SET @ddl := IF(@has_fk = 1 AND @new_rows = 0,
    'ALTER TABLE material DROP FOREIGN KEY fk_material_source_material', 'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND INDEX_NAME = 'idx_material_source_material');
SET @ddl := IF(@has_idx > 0 AND @new_rows = 0,
    'ALTER TABLE material DROP INDEX idx_material_source_material', 'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND COLUMN_NAME = 'source_material_id');
SET @ddl := IF(@has_col = 1 AND @new_rows = 0,
    'ALTER TABLE material DROP COLUMN source_material_id', 'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @new_price_rows := (SELECT COUNT(*) FROM material_price WHERE source = 'conversion');
SET @new_chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'material_price'
      AND tc.CONSTRAINT_NAME = 'chk_material_price_source' AND cc.CHECK_CLAUSE LIKE '%conversion%');
SET @ddl := IF(@new_chk = 1 AND @new_price_rows = 0,
    'ALTER TABLE material_price DROP CHECK chk_material_price_source', 'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_price' AND CONSTRAINT_NAME = 'chk_material_price_source');
SET @ddl := IF(@need_chk AND @new_price_rows = 0,
    'ALTER TABLE material_price ADD CONSTRAINT chk_material_price_source CHECK (source REGEXP ''^(manual|production_run|purchase)$'')',
    'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @new_chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'material_stock_movement'
      AND tc.CONSTRAINT_NAME = 'chk_msm_type' AND cc.CHECK_CLAUSE LIKE '%convert_in%');
SET @ddl := IF(@new_chk = 1 AND @new_rows = 0,
    'ALTER TABLE material_stock_movement DROP CHECK chk_msm_type', 'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND CONSTRAINT_NAME = 'chk_msm_type');
SET @ddl := IF(@need_chk AND @new_rows = 0,
    'ALTER TABLE material_stock_movement ADD CONSTRAINT chk_msm_type CHECK (movement_type REGEXP ''^(receipt|receipt_production|issue_production|issue_sample|return_production|return_sample|adjustment|writeoff)$'')',
    'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND CONSTRAINT_NAME = 'chk_msm_loss');
SET @ddl := IF(@has_chk = 1 AND @new_rows = 0,
    'ALTER TABLE material_stock_movement DROP CHECK chk_msm_loss', 'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND CONSTRAINT_NAME = 'fk_msm_conversion');
SET @ddl := IF(@has_fk = 1 AND @new_rows = 0,
    'ALTER TABLE material_stock_movement DROP FOREIGN KEY fk_msm_conversion', 'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND INDEX_NAME = 'idx_msm_conversion');
SET @ddl := IF(@has_idx > 0 AND @new_rows = 0,
    'ALTER TABLE material_stock_movement DROP INDEX idx_msm_conversion', 'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND COLUMN_NAME = 'loss_qty');
SET @ddl := IF(@has_col = 1 AND @new_rows = 0,
    'ALTER TABLE material_stock_movement DROP COLUMN loss_qty', 'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND COLUMN_NAME = 'conversion_id');
SET @ddl := IF(@has_col = 1 AND @new_rows = 0,
    'ALTER TABLE material_stock_movement DROP COLUMN conversion_id', 'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @ddl := IF(@new_rows = 0, 'DROP TABLE IF EXISTS material_conversion', 'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

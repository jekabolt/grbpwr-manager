-- +migrate Up

-- gap-07 v2 (D): structured lots / rolls (dye-lot) instead of the free-text `lot` string. A
-- `material_lot` is a received batch of a material with a supplier dye-lot code and a running
-- remaining quantity; a receipt can open/add to a lot and an issue can draw from one, so fabric can
-- be traced to the roll it was cut from and colour-matched across rolls.
--
-- IMPORTANT: this is TRACEABILITY, not a costing basis. Valuation stays the single moving-average
-- scalar on material_stock (lot.unit_cost is informational only). Issues are still priced at the
-- moving average — lots do NOT introduce FIFO / specific-identification costing. The free-text
-- material_stock_movement.lot column is kept for backward compatibility; movements now also carry a
-- structured lot_id.
--
-- Idempotent: CREATE TABLE IF NOT EXISTS + guarded ADD COLUMN/CONSTRAINT/INDEX.

CREATE TABLE IF NOT EXISTS material_lot (
    id INT AUTO_INCREMENT PRIMARY KEY,
    material_id INT NOT NULL,
    lot_code VARCHAR(64) NOT NULL,               -- supplier dye-lot / roll number
    supplier_doc VARCHAR(255) NULL,
    received_qty DECIMAL(12,3) NOT NULL DEFAULT 0,
    remaining_qty DECIMAL(12,3) NOT NULL DEFAULT 0,
    unit_cost DECIMAL(12,4) NULL,                -- informational, purchase currency (NOT a valuation basis)
    currency CHAR(3) NULL,
    received_at DATE NULL,
    note VARCHAR(255) NULL,
    archived BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT fk_material_lot_material FOREIGN KEY (material_id) REFERENCES material(id) ON DELETE CASCADE,
    CONSTRAINT uniq_material_lot_code UNIQUE (material_id, lot_code),
    CONSTRAINT chk_material_lot_qty CHECK (received_qty >= 0 AND remaining_qty >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND COLUMN_NAME = 'lot_id');
SET @sql := IF(@need_col,
    'ALTER TABLE material_stock_movement ADD COLUMN lot_id INT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement'
      AND CONSTRAINT_NAME = 'fk_msm_lot');
SET @sql := IF(@need_fk,
    'ALTER TABLE material_stock_movement
        ADD CONSTRAINT fk_msm_lot FOREIGN KEY (lot_id) REFERENCES material_lot(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_idx := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND INDEX_NAME = 'idx_msm_lot');
SET @sql := IF(@need_idx,
    'ALTER TABLE material_stock_movement ADD INDEX idx_msm_lot (lot_id)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

SET @has_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement'
      AND CONSTRAINT_NAME = 'fk_msm_lot');
SET @sql := IF(@has_fk,
    'ALTER TABLE material_stock_movement DROP FOREIGN KEY fk_msm_lot',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

DROP TABLE IF EXISTS material_lot;
-- (leaves the lot_id column; Down is not exercised in prod automigrate)
SELECT 1;

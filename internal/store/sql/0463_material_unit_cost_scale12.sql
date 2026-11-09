-- +migrate Up

-- Warehouse unit costs at twelve fractional digits instead of four (w1107 №221).
-- The moving average was rounded to 4 places on every receipt, so on_hand × avg drifted from the
-- money actually received: up to 0.00005 per unit per rounding, i.e. cents on a few thousand
-- metres, compounding with each receipt. Stock value stays 2 places and is computed from the
-- full-precision average (avgScale = 12 in internal/store/inventory).
--
-- data-checked: widening DECIMAL(12,4) → DECIMAL(24,12) keeps the integer part (8 → 12 digits) and
-- every existing value exactly; nullability and defaults are restated unchanged.
--
-- material_price.price widens too: a conversion copies its output unit cost into the price history
-- (10 € over 30 000 pcs = 0.000333333333 €; at 4 places a costing would see 9 €).
--
-- Idempotent: each table is altered only while one of its listed columns still has scale 4.
-- During the deploy overlap an old pod still rounds to 4 places, so a receipt posted in that window
-- keeps a 4-place average; rows written before 0463 keep their 4-place averages (no revaluation).

SET @n := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock'
      AND COLUMN_NAME IN ('avg_unit_cost_base') AND NUMERIC_SCALE = 4);
SET @ddl := IF(@n > 0, 'ALTER TABLE material_stock
        MODIFY COLUMN avg_unit_cost_base DECIMAL(24,12) NULL', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @n := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement'
      AND COLUMN_NAME IN ('unit_cost', 'unit_cost_base') AND NUMERIC_SCALE = 4);
SET @ddl := IF(@n > 0, 'ALTER TABLE material_stock_movement
        MODIFY COLUMN unit_cost DECIMAL(24,12) NULL,
        MODIFY COLUMN unit_cost_base DECIMAL(24,12) NULL', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @n := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot'
      AND COLUMN_NAME IN ('unit_cost') AND NUMERIC_SCALE = 4);
SET @ddl := IF(@n > 0, 'ALTER TABLE material_lot
        MODIFY COLUMN unit_cost DECIMAL(24,12) NULL', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @n := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_conversion'
      AND COLUMN_NAME IN ('output_unit_cost_base', 'output_avg_before') AND NUMERIC_SCALE = 4);
SET @ddl := IF(@n > 0, 'ALTER TABLE material_conversion
        MODIFY COLUMN output_unit_cost_base DECIMAL(24,12) NOT NULL DEFAULT 0,
        MODIFY COLUMN output_avg_before DECIMAL(24,12) NULL', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @n := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot_shrinkage'
      AND COLUMN_NAME IN ('avg_before', 'avg_after') AND NUMERIC_SCALE = 4);
SET @ddl := IF(@n > 0, 'ALTER TABLE material_lot_shrinkage
        MODIFY COLUMN avg_before DECIMAL(24,12) NULL,
        MODIFY COLUMN avg_after DECIMAL(24,12) NULL', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @n := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_price'
      AND COLUMN_NAME IN ('price') AND NUMERIC_SCALE = 4);
SET @ddl := IF(@n > 0, 'ALTER TABLE material_price
        MODIFY COLUMN price DECIMAL(24,12) NOT NULL', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- Back to four places (values are rounded; the integer part must still fit 8 digits).
SET @n := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_price'
      AND COLUMN_NAME IN ('price') AND NUMERIC_SCALE = 12);
SET @ddl := IF(@n > 0, 'ALTER TABLE material_price
        MODIFY COLUMN price DECIMAL(12,4) NOT NULL', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @n := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot_shrinkage'
      AND COLUMN_NAME IN ('avg_before', 'avg_after') AND NUMERIC_SCALE = 12);
SET @ddl := IF(@n > 0, 'ALTER TABLE material_lot_shrinkage
        MODIFY COLUMN avg_before DECIMAL(12,4) NULL,
        MODIFY COLUMN avg_after DECIMAL(12,4) NULL', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @n := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_conversion'
      AND COLUMN_NAME IN ('output_unit_cost_base', 'output_avg_before') AND NUMERIC_SCALE = 12);
SET @ddl := IF(@n > 0, 'ALTER TABLE material_conversion
        MODIFY COLUMN output_unit_cost_base DECIMAL(12,4) NOT NULL DEFAULT 0,
        MODIFY COLUMN output_avg_before DECIMAL(12,4) NULL', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @n := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot'
      AND COLUMN_NAME IN ('unit_cost') AND NUMERIC_SCALE = 12);
SET @ddl := IF(@n > 0, 'ALTER TABLE material_lot
        MODIFY COLUMN unit_cost DECIMAL(12,4) NULL', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @n := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement'
      AND COLUMN_NAME IN ('unit_cost', 'unit_cost_base') AND NUMERIC_SCALE = 12);
SET @ddl := IF(@n > 0, 'ALTER TABLE material_stock_movement
        MODIFY COLUMN unit_cost DECIMAL(12,4) NULL,
        MODIFY COLUMN unit_cost_base DECIMAL(12,4) NULL', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @n := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock'
      AND COLUMN_NAME IN ('avg_unit_cost_base') AND NUMERIC_SCALE = 12);
SET @ddl := IF(@n > 0, 'ALTER TABLE material_stock
        MODIFY COLUMN avg_unit_cost_base DECIMAL(12,4) NULL', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

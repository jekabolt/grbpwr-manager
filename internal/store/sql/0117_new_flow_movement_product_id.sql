-- +migrate Up

-- gap-07 v2 (C): per-colourway cost on a production run. A material issue to a run can now name the
-- colour-model (product) it was cut for, so the run's actual material cost breaks down per colourway
-- instead of being one style-level scalar. product_id is nullable: an issue left unattributed (shared
-- fabric, or the operator didn't specify) stays in the run's shared bucket. ON DELETE SET NULL — a
-- product removal never deletes the movement ledger. Sales COGS is unaffected: this reads the same
-- issue movements, it only groups them.
--
-- Idempotent: guarded ADD COLUMN + ADD CONSTRAINT + ADD INDEX via information_schema.

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND COLUMN_NAME = 'product_id');
SET @sql := IF(@need_col,
    'ALTER TABLE material_stock_movement ADD COLUMN product_id INT NULL',
    'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement'
      AND CONSTRAINT_NAME = 'fk_msm_product');
SET @sql := IF(@need_fk,
    'ALTER TABLE material_stock_movement
        ADD CONSTRAINT fk_msm_product FOREIGN KEY (product_id) REFERENCES product(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

SET @need_idx := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND INDEX_NAME = 'idx_msm_product');
SET @sql := IF(@need_idx,
    'ALTER TABLE material_stock_movement ADD INDEX idx_msm_product (product_id)',
    'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- +migrate Down

SET @has_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement'
      AND CONSTRAINT_NAME = 'fk_msm_product');
SET @sql := IF(@has_fk,
    'ALTER TABLE material_stock_movement DROP FOREIGN KEY fk_msm_product',
    'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;
-- (leaves the product_id column + index; Down is not exercised in prod automigrate)
SELECT 1;

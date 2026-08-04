-- +migrate Up

-- production-costing Phases 8+9 (plans 04, 12, 13): audit trails + procurement quick wins.
--
-- 1. product_cost_event — append-only history of every cost_price write (plan 12.2). Closes the
--    "reversal cannot know the previous value" gap for good and gives cost provenance an audit
--    stream. Write-only in v1 (read surface comes with a later UI pass), mirroring how the 0234
--    cost-doc columns landed write-only first.
CREATE TABLE IF NOT EXISTS product_cost_event (
    id INT AUTO_INCREMENT PRIMARY KEY,
    product_id INT NOT NULL,
    cost_before DECIMAL(10, 2) NULL,
    cost_after DECIMAL(10, 2) NULL,
    source VARCHAR(32) NOT NULL,
    source_ref VARCHAR(64) NULL,
    actor VARCHAR(255) NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_pce_product FOREIGN KEY (product_id) REFERENCES product (id) ON DELETE CASCADE,
    INDEX idx_pce_product_created (product_id, created_at)
);

-- 2. Procurement quick wins (plan 13 §1, PO entity deliberately cut by review 14): the supplier and
--    lead time live on the MATERIAL (v1 = one blended supplier per material), and a receipt can
--    carry the date the delivery was promised for (expected_at) — enough for "what is late"
--    without inventing purchase orders nobody will fill in.
SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND COLUMN_NAME = 'supplier_id');
SET @sql := IF(@have_col = 0,
    'ALTER TABLE material ADD COLUMN supplier_id INT NULL, ADD COLUMN lead_time_days INT NULL,
     ADD CONSTRAINT fk_material_supplier FOREIGN KEY (supplier_id) REFERENCES supplier (id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND COLUMN_NAME = 'expected_at');
SET @sql := IF(@have_col = 0,
    'ALTER TABLE material_stock_movement ADD COLUMN expected_at DATETIME NULL COMMENT ''when this receipt was promised to arrive (Phase 9)''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 3. production_run.supplier_id — which factory runs the batch (plan 12.1). Nullable; the FK
--    mirrors material_stock_movement.supplier_id (0201). Unlocks per-vendor variance/defect
--    reporting without any engine.
SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run' AND COLUMN_NAME = 'supplier_id');
SET @sql := IF(@have_col = 0,
    'ALTER TABLE production_run ADD COLUMN supplier_id INT NULL,
     ADD CONSTRAINT fk_prun_supplier FOREIGN KEY (supplier_id) REFERENCES supplier (id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- The FK'd columns and the audit table are load-bearing once written; narrowing back would drop
-- history. Intentional no-op.
SELECT 1;

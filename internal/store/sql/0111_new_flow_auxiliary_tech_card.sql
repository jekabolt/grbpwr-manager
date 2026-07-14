-- +migrate Up

-- new-flow NF-07: auxiliary items (dust bags, garment bags, shoppers). These are not sold — they
-- pack the products we sell — but they have a full tech card (BOM, operations, samples, runs). An
-- auxiliary card's production output is received NOT into a product's stock but INTO the material
-- warehouse (NF-01): the finished dust bag becomes a `packaging` material that is later consumed
-- packing orders. No new warehouse entity — we reuse material_stock.
--
-- tech_card.purpose distinguishes the two; output_material_id is the material an auxiliary card's
-- run receipts land in (required before the first run, enforced in the service layer, not the DB —
-- a card can be drafted before its output material exists).
--
-- Idempotent: guarded ADD COLUMN (MySQL 8 has no ADD COLUMN IF NOT EXISTS) + named FK/CHECK added
-- only when absent, so a mid-file DDL failure re-runs cleanly from the top.

SET @need_cols := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'purpose');
SET @sql := IF(@need_cols,
    'ALTER TABLE tech_card
        ADD COLUMN purpose VARCHAR(16) NOT NULL DEFAULT ''sellable'',
        ADD COLUMN output_material_id INT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND CONSTRAINT_NAME = 'fk_tech_card_output_material');
SET @sql := IF(@need_fk,
    'ALTER TABLE tech_card ADD CONSTRAINT fk_tech_card_output_material FOREIGN KEY (output_material_id) REFERENCES material(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND CONSTRAINT_NAME = 'chk_tech_card_purpose');
SET @sql := IF(@need_chk,
    'ALTER TABLE tech_card ADD CONSTRAINT chk_tech_card_purpose CHECK (purpose REGEXP ''^(sellable|auxiliary)$'')',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- (leaves purpose/output_material_id; a Down is not exercised in prod automigrate)
SELECT 1;

-- +migrate Up

-- production-costing Phase 3 (plan 11): stored price provenance on BOM lines. The costing tab
-- promises "snapshot / catalog / date" evidence per material line, but a BOM row knows only
-- unit_price + currency — WHERE the price came from and WHEN it was stamped were unknowable
-- (codex#9). Two nullable columns record it:
--   price_source      'manual'  — the operator typed/edited the price through a card save;
--                     'catalog' — the price was pulled from the material catalog by the reprice
--                                 action (RepriceTechCardBom), matching the estimate's
--                                 CATALOG_LATEST resolution;
--                     NULL      — pre-provenance row, honestly unknown (no fake backfill: nothing
--                                 records when legacy prices were typed).
--   price_snapshot_at when that source stamped the price (UTC).
-- The columns are metadata, NOT part of the signed MATERIALS digest projection — adding them to the
-- digest would mass-stale every approved card for a byte-layout change (review S1 rule).

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'price_source');
SET @sql := IF(@have_col = 0,
    'ALTER TABLE tech_card_bom_item ADD COLUMN price_source VARCHAR(16) NULL COMMENT ''manual | catalog; NULL = unknown (pre-provenance)''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'price_snapshot_at');
SET @sql := IF(@have_col = 0,
    'ALTER TABLE tech_card_bom_item ADD COLUMN price_snapshot_at TIMESTAMP NULL COMMENT ''when price_source stamped unit_price (UTC)''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND CONSTRAINT_NAME = 'chk_bom_price_source');
SET @sql := IF(@have_chk = 0,
    'ALTER TABLE tech_card_bom_item ADD CONSTRAINT chk_bom_price_source CHECK (price_source IS NULL OR price_source REGEXP ''^(manual|catalog)$'')',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

SET @have_chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND CONSTRAINT_NAME = 'chk_bom_price_source');
SET @sql := IF(@have_chk > 0,
    'ALTER TABLE tech_card_bom_item DROP CHECK chk_bom_price_source',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'price_snapshot_at');
SET @sql := IF(@have_col > 0,
    'ALTER TABLE tech_card_bom_item DROP COLUMN price_snapshot_at',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'price_source');
SET @sql := IF(@have_col > 0,
    'ALTER TABLE tech_card_bom_item DROP COLUMN price_source',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

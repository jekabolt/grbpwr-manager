-- +migrate Up

-- new-flow NF-02: extend the material catalog for warehouse use (internal article code, colour,
-- pantone, a low-stock threshold, free notes) and realign the section CHECK so it matches the Go
-- and proto enums (which already carry trim|decoration|other) — a long-standing drift where the
-- material.section CHECK only allowed 8 values and tech_card_bom_item.section only 9, so inserting
-- a `decoration`/`other`/`trim` material or BOM line failed at the DB.
--
-- MySQL 8 has no ADD COLUMN IF NOT EXISTS, and the existing section CHECKs are auto-named
-- (material_chk_N) which must not be dropped by positional name; both are handled dynamically via
-- information_schema so the whole file is idempotent (a mid-file DDL failure re-runs from the top).

-- --- material: new catalog columns (guarded on the first new column) ---
SET @need_cols := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND COLUMN_NAME = 'code');
SET @sql := IF(@need_cols,
    'ALTER TABLE material
        ADD COLUMN code VARCHAR(64) NULL,
        ADD COLUMN color VARCHAR(64) NULL,
        ADD COLUMN pantone VARCHAR(32) NULL,
        ADD COLUMN min_stock DECIMAL(12,3) NULL,
        ADD COLUMN notes TEXT NULL',
    'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- --- material.section: drop the auto-named CHECK, add the named, widened one ---
SET @cname := (
    SELECT tc.CONSTRAINT_NAME
    FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
        ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'material'
        AND tc.CONSTRAINT_TYPE = 'CHECK'
        AND cc.CHECK_CLAUSE LIKE '%section%'
        AND tc.CONSTRAINT_NAME <> 'chk_material_section'
    LIMIT 1);
SET @sql := IF(@cname IS NULL, 'SELECT 1', CONCAT('ALTER TABLE material DROP CHECK ', @cname));
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

SET @have := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND CONSTRAINT_NAME = 'chk_material_section');
SET @sql := IF(@have > 0, 'SELECT 1',
    'ALTER TABLE material ADD CONSTRAINT chk_material_section CHECK (section REGEXP ''^(fabric|lining|interlining|insulation|hardware|thread|label|packaging|trim|decoration|other)$'')');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- --- tech_card_bom_item.section: same realignment ---
SET @cname := (
    SELECT tc.CONSTRAINT_NAME
    FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
        ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_bom_item'
        AND tc.CONSTRAINT_TYPE = 'CHECK'
        AND cc.CHECK_CLAUSE LIKE '%section%'
        AND tc.CONSTRAINT_NAME <> 'chk_bom_item_section'
    LIMIT 1);
SET @sql := IF(@cname IS NULL, 'SELECT 1', CONCAT('ALTER TABLE tech_card_bom_item DROP CHECK ', @cname));
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

SET @have := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND CONSTRAINT_NAME = 'chk_bom_item_section');
SET @sql := IF(@have > 0, 'SELECT 1',
    'ALTER TABLE tech_card_bom_item ADD CONSTRAINT chk_bom_item_section CHECK (section REGEXP ''^(fabric|lining|interlining|insulation|hardware|thread|label|packaging|trim|decoration|other)$'')');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- index for the internal article lookup
SET @have_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND INDEX_NAME = 'idx_material_code');
SET @sql := IF(@have_idx > 0, 'SELECT 1', 'CREATE INDEX idx_material_code ON material (code)');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- +migrate Down

ALTER TABLE material
    DROP COLUMN code,
    DROP COLUMN color,
    DROP COLUMN pantone,
    DROP COLUMN min_stock,
    DROP COLUMN notes;

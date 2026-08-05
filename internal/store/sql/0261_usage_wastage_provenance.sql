-- +migrate Up

-- Consumption provenance on a colourway usage row (Ф9.4, PIECES-WASTAGE-DESIGN §2.3).
-- consumption_source says HOW the per-garment norm was set. 'manual' (default, every existing
-- row) keeps today's costing exactly, the article's wastage_percent grosses the cost up.
-- 'marker' means the norm was applied from a saved раскладка whose measured length already
-- CONTAINS the inter-piece cutting waste, and the selvedge rides the per-running-metre price
-- implicitly, so the read path must NOT gross such rows up again (the fix for the marker
-- double-count trap). waste_selvedge_pct / waste_cut_pct are the DISPLAY decomposition of the
-- marker's waste (кромка + рез), never multiplied into any cost. NULL on manual rows.
-- bom_item.wastage_percent itself is untouched, it stays the fallback estimate, and zeroing it
-- would stale the MATERIALS sign-off digest.
--
-- tech_card_marker.selvedge_cm snapshots the кромка the раскладка ran with (populated by the
-- nesting client from the effective article at save time), so a marker's waste decomposition
-- stays auditable after the material's selvedge changes.

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_colorway_usage'
      AND COLUMN_NAME = 'consumption_source'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_colorway_usage ADD COLUMN consumption_source VARCHAR(16) NOT NULL DEFAULT ''manual'' AFTER consumption, ADD CONSTRAINT chk_tccu_consumption_source CHECK (consumption_source IN (''manual'', ''marker''))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_colorway_usage'
      AND COLUMN_NAME = 'waste_selvedge_pct'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_colorway_usage ADD COLUMN waste_selvedge_pct DECIMAL(5,2) NULL AFTER consumption_source, ADD COLUMN waste_cut_pct DECIMAL(5,2) NULL AFTER waste_selvedge_pct, ADD CONSTRAINT chk_tccu_waste_selvedge CHECK (waste_selvedge_pct IS NULL OR (waste_selvedge_pct >= 0 AND waste_selvedge_pct <= 100)), ADD CONSTRAINT chk_tccu_waste_cut CHECK (waste_cut_pct IS NULL OR (waste_cut_pct >= 0 AND waste_cut_pct <= 100))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_marker'
      AND COLUMN_NAME = 'selvedge_cm'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_marker ADD COLUMN selvedge_cm DECIMAL(5,2) NOT NULL DEFAULT 0 AFTER edge_margin_cm, ADD CONSTRAINT chk_tcm_selvedge_nonneg CHECK (selvedge_cm >= 0)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_marker'
      AND COLUMN_NAME = 'selvedge_cm'
);
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card_marker DROP CONSTRAINT chk_tcm_selvedge_nonneg, DROP COLUMN selvedge_cm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_colorway_usage'
      AND COLUMN_NAME = 'waste_selvedge_pct'
);
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card_colorway_usage DROP CONSTRAINT chk_tccu_waste_selvedge, DROP CONSTRAINT chk_tccu_waste_cut, DROP COLUMN waste_selvedge_pct, DROP COLUMN waste_cut_pct',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_colorway_usage'
      AND COLUMN_NAME = 'consumption_source'
);
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card_colorway_usage DROP CONSTRAINT chk_tccu_consumption_source, DROP COLUMN consumption_source',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

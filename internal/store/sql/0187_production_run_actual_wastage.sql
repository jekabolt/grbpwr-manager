-- +migrate Up
-- "wastage% -> production run": the cutting wastage % moves from a fixed per-BOM-line estimate to a
-- per-RUN actual. It varies run to run with how tightly the marker/lay nests the pieces on the
-- fabric, so it is a property of the batch, not the style. The BOM line's wastage_percent stays as
-- the PLANNING ESTIMATE (the fallback); this column is the run's ACTUAL, entered once the marker is
-- known. When set it overrides the BOM estimate in the run's planned-cost snapshot and material
-- plan; NULL means "fall back to the BOM line's estimate". Nullable with a NULL default so every
-- existing run keeps using the BOM estimate -- prod-data-safe (no backfill, no behaviour change).
--
-- Idempotent: MySQL 8 has no ADD COLUMN IF NOT EXISTS, so guard on information_schema and ALTER only
-- when the column is absent, so a mid-file DDL failure (DDL auto-commits) re-runs cleanly from the
-- top. Mirrors the marker_efficiency_pct add in 0110 and the BOM wastage_percent CHECK in 0073.

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run' AND COLUMN_NAME = 'actual_wastage_percent');
SET @sql := IF(@need_col,
    'ALTER TABLE production_run
        ADD COLUMN actual_wastage_percent DECIMAL(5,2) NULL
            CHECK (actual_wastage_percent IS NULL OR (actual_wastage_percent >= 0 AND actual_wastage_percent <= 100))',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

SET @has_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run' AND COLUMN_NAME = 'actual_wastage_percent');
SET @sql := IF(@has_col,
    'ALTER TABLE production_run DROP COLUMN actual_wastage_percent',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

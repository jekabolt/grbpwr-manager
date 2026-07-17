-- +migrate Up

-- Contract decision R6: give a colourway (product) a single STORED lifecycle status instead of the
-- scattered (hidden, deleted_at) knobs and the generated `status` enum they fed. lifecycle_status is
-- the one authoritative source; it is written only through validated transitions (entity state
-- machine + store commands Publish/Hide/Unhide/Archive), never recomputed from other columns.
--
--   1 = draft     (created, not yet published; admin-visible, never on the storefront)
--   2 = active    (published, publicly visible)
--   3 = hidden    (admin-visible, temporarily off the storefront)
--   4 = archived  (terminal; deleted_at carries the archival audit timestamp)
--
-- Backfill (byte-for-byte equivalent to the old filters): deleted_at set -> 4; else hidden=1 -> 3;
-- else -> 2. There are no drafts in legacy data — draft rows are minted by the colourway merge (0143)
-- and by CreateColorway. `hidden` is dropped; `deleted_at` REMAINS as the archival audit stamp (never
-- a lifecycle filter anymore). published_at is an audit stamp of first publication, NOT a status source.
--
-- Idempotent: guarded ADD/MODIFY/DROP via information_schema (multi-line PREPARE/EXECUTE/DEALLOCATE —
-- a single line trips 1064 on the managed DSN, see 0124). The backfill that reads `hidden` is itself
-- guarded on `hidden` still existing, so a re-run after the column was dropped is a no-op. The CHECK is
-- named explicitly (never an auto-generated <table>_chk_<n>) so drops resolve by a stable name.

-- 1) Stored status column, nullable during backfill.
SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'lifecycle_status');
SET @sql := IF(@need_col,
    'ALTER TABLE product ADD COLUMN lifecycle_status TINYINT UNSIGNED NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 2) Backfill from the legacy knobs — guarded on `hidden` still existing so a retried partial apply
--    (which may already have dropped `hidden`) does not fail on an unknown column.
SET @has_hidden := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'hidden');
SET @sql := IF(@has_hidden > 0,
    'UPDATE product SET lifecycle_status = CASE
         WHEN deleted_at IS NOT NULL THEN 4
         WHEN hidden = 1 THEN 3
         ELSE 2 END
     WHERE lifecycle_status IS NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 3) Enforce NOT NULL and set the safe default (a freshly inserted colourway of unknown provenance is
--    a draft, never auto-published). The Go create path sets the value explicitly.
SET @is_nullable := (SELECT IS_NULLABLE FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'lifecycle_status');
SET @sql := IF(@is_nullable = 'YES',
    'ALTER TABLE product MODIFY COLUMN lifecycle_status TINYINT UNSIGNED NOT NULL DEFAULT 1',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 4) Named CHECK: only 1..4 are storable (UNKNOWN=0 is never written).
SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product'
      AND CONSTRAINT_NAME = 'chk_product_lifecycle_status');
SET @sql := IF(@need_chk,
    'ALTER TABLE product ADD CONSTRAINT chk_product_lifecycle_status CHECK (lifecycle_status BETWEEN 1 AND 4)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 5) Publication audit stamp (nullable; NOT a status source).
SET @need_pub := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'published_at');
SET @sql := IF(@need_pub,
    'ALTER TABLE product ADD COLUMN published_at TIMESTAMP NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Best-effort audit backfill: every non-draft legacy row was published at some point; created_at is the
-- only available proxy. Gated on published_at IS NULL so a re-run is a no-op.
UPDATE product SET published_at = created_at
    WHERE published_at IS NULL AND lifecycle_status <> 1;

-- 6) Drop the legacy generated `status` enum FIRST (it is computed from hidden/deleted_at, so it must go
--    before `hidden` can be dropped). Only present if the pre-R6 0137 ever ran (e.g. on beta); a no-op
--    on a fresh apply. Dropping the column drops its idx_product_status index with it.
SET @has_status := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'status');
SET @sql := IF(@has_status > 0, 'ALTER TABLE product DROP COLUMN status', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 7) Drop the legacy `hidden` column now that lifecycle_status owns the state.
SET @has_hidden := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'hidden');
SET @sql := IF(@has_hidden > 0, 'ALTER TABLE product DROP COLUMN hidden', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 8) Index for lifecycle-scoped reads (storefront lifecycle_status = 2, admin <> 4).
SET @need_idx := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND INDEX_NAME = 'idx_product_lifecycle_status');
SET @sql := IF(@need_idx,
    'ALTER TABLE product ADD INDEX idx_product_lifecycle_status (lifecycle_status)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

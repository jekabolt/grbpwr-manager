-- +migrate Up

-- Contract decision R2 (variant identity): a variant (product_size) is archive-not-delete. An order line,
-- waitlist row and stock-history row all address the immutable product_size.id, so a variant referenced by
-- history can never be physically removed — it is retired by flipping a stored lifecycle flag instead.
-- This gives product_size the single stored status the admin Variant CRUD (CreateVariant/UpdateVariant/
-- ArchiveVariant) and UpdateVariantStock read: an archived variant rejects stock writes (FAILED_PRECONDITION)
-- and drops off the storefront, while its id stays valid for the frozen order/stock references.
--
--   1 = active    (default; the only state a fresh variant is created in)
--   2 = archived  (retired; excluded from storefront, rejects stock writes; id stays referenceable)
--
-- Two states are deliberate — a variant needs only "sellable" vs "retired"; the richer colourway lifecycle
-- (draft/active/hidden/archived) lives on product.lifecycle_status (0137). Wire enum VariantLifecycleStatus
-- {UNKNOWN=0, ACTIVE=1, ARCHIVED=2}; UNKNOWN(0) is never stored (fail-closed on an unknown read).
--
-- Backfill: every existing product_size is a live variant -> active(1). Idempotent: the ADD/MODIFY are
-- guarded via information_schema (multi-line PREPARE/EXECUTE/DEALLOCATE — a single-line trio trips 1064 on
-- the managed DSN, see 0124), the backfill only touches NULL rows, and the CHECK is named explicitly (never
-- an auto-generated <table>_chk_<n>) so a later drop resolves by a stable name. 0151/0152 are not edited.

-- 1) Stored status column, nullable during backfill.
SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product_size' AND COLUMN_NAME = 'status');
SET @sql := IF(@need_col,
    'ALTER TABLE product_size ADD COLUMN status TINYINT UNSIGNED NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 2) Backfill: all existing variants are active. Idempotent — only NULL rows are touched, so a retried
--    apply after step 3 has set the column NOT NULL matches nothing.
UPDATE product_size SET status = 1 WHERE status IS NULL;

-- 3) Enforce NOT NULL and the safe default (a fresh variant is created active). The Go create path relies
--    on this default; ArchiveVariant flips it to 2.
SET @is_nullable := (SELECT IS_NULLABLE FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product_size' AND COLUMN_NAME = 'status');
SET @sql := IF(@is_nullable = 'YES',
    'ALTER TABLE product_size MODIFY COLUMN status TINYINT UNSIGNED NOT NULL DEFAULT 1',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 4) Named CHECK: only 1..2 are storable (UNKNOWN=0 is never written). File ends on a statement (sql-migrate
--    requires the last non-empty line to terminate a statement, not a comment).
SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product_size'
      AND CONSTRAINT_NAME = 'chk_product_size_status');
SET @sql := IF(@need_chk,
    'ALTER TABLE product_size ADD CONSTRAINT chk_product_size_status CHECK (status BETWEEN 1 AND 2)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

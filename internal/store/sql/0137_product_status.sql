-- +migrate Up

-- PR5-A: give a product a single, authoritative lifecycle status instead of the scattered
-- (deleted_at, hidden) pair whose interaction was only ever expressed ad-hoc in each WHERE clause
-- ("hidden = 0 AND deleted_at IS NULL"). `status` is a STORED generated column: it is COMPUTED from
-- the existing knobs, so it cannot drift from them and needs no write-path changes — the interaction
-- precedence (archived beats hidden beats active) now lives in exactly one place. Reads switch to
-- filtering on `status`; the operator still toggles `hidden` / soft-deletes via `deleted_at` as
-- before, and the status recomputes automatically.
--
-- Mapping (byte-for-byte equivalent to the old filters on real data — `hidden` defaults FALSE and is
-- always set on insert, so NULL never occurs in practice; NULL maps to 'active', matching the column
-- default and the metrics layer's (hidden IS NULL OR hidden = 0) treatment):
--   deleted_at IS NOT NULL        -> 'archived'   (old: excluded from every storefront/admin read)
--   hidden = 1                    -> 'hidden'     (old: hidden = 1, admin-only)
--   else                          -> 'active'     (old: hidden = 0 AND deleted_at IS NULL)
-- so storefront `status = 'active'` == old `hidden = 0 AND deleted_at IS NULL`, and admin-with-hidden
-- `status <> 'archived'` == old `deleted_at IS NULL`.
--
-- preorder / sold_out / hidden_for_non_qualified are deliberately NOT folded in: they are orthogonal
-- axes (availability window, derived stock state, tier gating), not lifecycle states.
--
-- Idempotent: guarded ADD COLUMN / ADD INDEX via information_schema (multi-line PREPARE/EXECUTE/
-- DEALLOCATE — a single line trips 1064 on the managed DSN, see 0124). A STORED generated column
-- backfills itself on ADD, so there is no separate backfill step.

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'status');
SET @sql := IF(@need_col,
    'ALTER TABLE product ADD COLUMN status ENUM(''active'',''hidden'',''archived'')
        GENERATED ALWAYS AS (
            CASE WHEN deleted_at IS NOT NULL THEN ''archived''
                 WHEN hidden = 1 THEN ''hidden''
                 ELSE ''active'' END
        ) STORED NOT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_idx := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND INDEX_NAME = 'idx_product_status');
SET @sql := IF(@need_idx,
    'ALTER TABLE product ADD INDEX idx_product_status (status)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

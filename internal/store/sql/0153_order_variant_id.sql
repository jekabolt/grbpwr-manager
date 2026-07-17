-- +migrate Up

-- R2 / problem 019: an order line must address a stable, atomic variant identity, not the mutable
-- (product_id, size_id) pair. A colourway edit that delete+reinserted a product_size used to change the
-- DB id under a live line, so order history and stock could drift. The immutable product_size.id is the
-- correct anchor.
--
-- This adds order_item.variant_id (FK RESTRICT, NOT NULL) ALONGSIDE the existing product_id/size_id
-- columns. The pair is deliberately kept: the metrics package and the label/stock/re-snapshot paths read
-- (product_id, size_id), and that pair is UNIQUE on product_size so it resolves to exactly the same
-- variant — variant_id is the new canonical key, the pair a denormalised read convenience. It also
-- renames the immutable snapshot product_sku -> variant_sku_snapshot and adds base_sku_snapshot (the
-- [:14] base SKU) so GA4 / order history / labels read the frozen identity directly.
--
-- Prod has 0 orders, so the backfills only ever fill forward and the NOT NULL + FK are the integrity
-- guards (a bad pair halts the migration rather than silently writing NULL). Idempotent: every DDL is
-- guarded via information_schema with multi-line PREPARE/EXECUTE/DEALLOCATE (a single-line trio trips
-- 1064 on the managed DSN, see 0124). 0151/0152 are not edited.

-- 1) order_item.variant_id column (nullable first so the backfill can populate it).
SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item' AND COLUMN_NAME = 'variant_id');
SET @sql := IF(@need_col,
    'ALTER TABLE order_item ADD COLUMN variant_id INT NULL DEFAULT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 2) Backfill variant_id from the (product_id, size_id) pair (UNIQUE on product_size). Idempotent: only
--    NULL rows are touched.
UPDATE order_item oi
JOIN product_size ps ON ps.product_id = oi.product_id AND ps.size_id = oi.size_id
SET oi.variant_id = ps.id
WHERE oi.variant_id IS NULL;

-- 3) Enforce NOT NULL (guarded). A remaining NULL means an order line whose pair has no product_size
--    row; with 0 orders this never fires, and if it did the MODIFY halts the migration fail-fast.
SET @is_nullable := (SELECT IS_NULLABLE FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item' AND COLUMN_NAME = 'variant_id');
SET @sql := IF(@is_nullable = 'YES',
    'ALTER TABLE order_item MODIFY COLUMN variant_id INT NOT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 4) FK RESTRICT to product_size(id): a variant referenced by any order line can no longer be physically
--    deleted (archive instead). Added only if absent.
SET @has_fk := (SELECT COUNT(*) FROM information_schema.KEY_COLUMN_USAGE
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item'
      AND COLUMN_NAME = 'variant_id' AND REFERENCED_TABLE_NAME = 'product_size');
SET @sql := IF(@has_fk = 0,
    'ALTER TABLE order_item ADD CONSTRAINT fk_order_item_variant FOREIGN KEY (variant_id) REFERENCES product_size(id) ON DELETE RESTRICT',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 5) Rename the immutable snapshot product_sku -> variant_sku_snapshot (keeps VARCHAR(64) NOT NULL from
--    0150). Guarded so a re-run after the rename is a no-op.
SET @has_old_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item' AND COLUMN_NAME = 'product_sku');
SET @has_new_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item' AND COLUMN_NAME = 'variant_sku_snapshot');
SET @sql := IF(@has_old_col = 1 AND @has_new_col = 0,
    'ALTER TABLE order_item CHANGE COLUMN product_sku variant_sku_snapshot VARCHAR(64) NOT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 6) Rename the snapshot index to match (the column rename in step 5 leaves the index on the new column
--    but keeps its old name).
SET @has_old_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item' AND INDEX_NAME = 'idx_order_item_product_sku');
SET @has_new_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item' AND INDEX_NAME = 'idx_order_item_variant_sku_snapshot');
SET @sql := IF(@has_old_idx > 0 AND @has_new_idx = 0,
    'ALTER TABLE order_item RENAME INDEX idx_order_item_product_sku TO idx_order_item_variant_sku_snapshot',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 7) base_sku_snapshot: the frozen [:14] base SKU (SS26-00021-BLK) for the product-page URL and GA4
--    item_group_id. Nullable (a legacy sentinel snapshot may be shorter); read paths COALESCE to ''.
SET @need_base := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item' AND COLUMN_NAME = 'base_sku_snapshot');
SET @sql := IF(@need_base,
    'ALTER TABLE order_item ADD COLUMN base_sku_snapshot VARCHAR(64) NULL DEFAULT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Backfill base_sku_snapshot = first 14 chars of the variant SKU snapshot. Idempotent: only NULL rows
-- with a snapshot long enough to carry a base segment are touched. File ends on a statement (sql-migrate
-- requires the last non-empty line to terminate a statement, not a comment).
UPDATE order_item
SET base_sku_snapshot = LEFT(variant_sku_snapshot, 14)
WHERE base_sku_snapshot IS NULL AND variant_sku_snapshot IS NOT NULL AND CHAR_LENGTH(variant_sku_snapshot) >= 14;

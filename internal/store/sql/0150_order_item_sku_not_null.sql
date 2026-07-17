-- +migrate Up

-- Problem 023: order_item.product_sku is the immutable variant-SKU snapshot of a sold line, but it was
-- nullable (0135) and the read paths fell back to the LIVE variant / base SKU
-- (COALESCE(oi.product_sku, ps.sku, p.sku)). So a sale could be recorded without a stable identity, and
-- a later catalogue remint would silently rewrite what order history and labels showed as the sold SKU.
--
-- The application now hard-fails a sale (submit/payment/custom/label) that has no live variant SKU, and
-- the read fallbacks are removed, so new rows are always populated from the frozen snapshot. This
-- migration backfills any legacy NULL/empty rows once — the ONLY place a live/base fallback survives —
-- and then enforces NOT NULL.
--
-- Backfill order: current variant SKU, else the product base SKU, else a marked sentinel so a genuinely
-- unrecoverable historical row cannot block NOT NULL. order_item.product_id is a FK (the product always
-- exists), so the base SKU is available for all but truly unminted products. Idempotent: the UPDATE only
-- touches NULL/empty rows and the MODIFY is guarded via information_schema.

UPDATE order_item oi
JOIN product p ON p.id = oi.product_id
LEFT JOIN product_size ps ON ps.product_id = oi.product_id AND ps.size_id = oi.size_id
SET oi.product_sku = COALESCE(NULLIF(oi.product_sku, ''), NULLIF(ps.sku, ''), NULLIF(p.sku, ''), CONCAT('LEGACY-UNKNOWN-', oi.id))
WHERE oi.product_sku IS NULL OR oi.product_sku = '';

SET @is_nullable := (SELECT IS_NULLABLE FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item' AND COLUMN_NAME = 'product_sku');
SET @sql := IF(@is_nullable = 'YES',
    'ALTER TABLE order_item MODIFY COLUMN product_sku VARCHAR(64) NOT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Up

-- SKU redesign task 15: snapshot the variant SKU onto the order line. Until now order_item carried no
-- SKU and every read derived it via a live JOIN to product.sku — so a later product edit would rewrite
-- order history. product_sku freezes the variant SKU (product_size.sku) at the moment the line is
-- written, mirroring the price/cost/VAT/promo snapshots added for the same reason. It is filled at
-- order_item insert and re-snapshotted at OrderPaymentDone (the freeze point — see task 07).
--
-- Prod has 0 orders, so there is nothing to backfill — the column only ever fills forward.
--
-- Idempotent: guarded ADD COLUMN + index via information_schema (multi-line PREPARE/EXECUTE/DEALLOCATE
-- — a single line trips 1064 on the managed DSN, see 0124).

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item' AND COLUMN_NAME = 'product_sku');
SET @sql := IF(@need_col,
    'ALTER TABLE order_item ADD COLUMN product_sku VARCHAR(64) NULL DEFAULT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_idx := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item' AND INDEX_NAME = 'idx_order_item_product_sku');
SET @sql := IF(@need_idx,
    'ALTER TABLE order_item ADD INDEX idx_order_item_product_sku (product_sku)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Up

-- analytics-v2 task 05: snapshot the applied promo on the order so historical discount and
-- reconstructed-revenue figures stop being rewritten when a promo_code is later edited or deleted.
-- Until now the only promo link was customer_order.promo_id, and every metric reconstructed the
-- discount via a live JOIN to promo_code.discount / .free_shipping — so editing a code retroactively
-- changed past revenue, and cancelling an order (which NULLs promo_id) erased the link entirely.
-- This mirrors the price / cost / VAT snapshots added earlier for the same reason.
--
-- promo_discount_pct: the percentage at apply time. promo_free_shipping: the free-shipping flag.
-- promo_code_snapshot: the code string, so promo reports survive the code's deletion.
--
-- Idempotent: guarded ADD COLUMN via information_schema; backfill only fills still-NULL snapshots.

SET @need_pct := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'customer_order' AND COLUMN_NAME = 'promo_discount_pct');
SET @sql := IF(@need_pct,
    'ALTER TABLE customer_order ADD COLUMN promo_discount_pct DECIMAL(5,2) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_fs := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'customer_order' AND COLUMN_NAME = 'promo_free_shipping');
SET @sql := IF(@need_fs,
    'ALTER TABLE customer_order ADD COLUMN promo_free_shipping BOOLEAN NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_code := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'customer_order' AND COLUMN_NAME = 'promo_code_snapshot');
SET @sql := IF(@need_code,
    'ALTER TABLE customer_order ADD COLUMN promo_code_snapshot VARCHAR(64) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Backfill existing promo orders from the current catalogue (the best available approximation for
-- history). Only fills rows that have a promo but no snapshot yet, so a re-run is a no-op.
UPDATE customer_order co
JOIN promo_code pc ON pc.id = co.promo_id
SET co.promo_discount_pct = pc.discount,
    co.promo_free_shipping = pc.free_shipping,
    co.promo_code_snapshot = pc.code
WHERE co.promo_id IS NOT NULL AND co.promo_discount_pct IS NULL;

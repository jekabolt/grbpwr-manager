-- +migrate Up
-- Description: repair the schema drift of databases inherited from the old grbpwr prod (lk_grbpwr):
-- address.country_code + its index missing, review rating columns NOT NULL, order_item FK index names.
-- compat: safe NOT NULL to NULL DEFAULT NULL only widens; older code writes these columns as before
--
-- A database built by the migration chain already has the shape below, so every block is guarded by
-- information_schema and does nothing there. Only the drifted databases change:
--
-- 1. address.country_code CHAR(2) NULL + idx_address_country_code (0053) are missing, while
--    accounting/orderfacts.go and vatreturn.go read a.country_code. The column is added AFTER country
--    and backfilled exactly as 0053 did. The backfill runs only while the index is still missing and
--    the index is created last, so a run killed after ADD COLUMN backfills again on the retry, and a
--    database that already has the index (every chain-built one) is not touched. No code writes
--    address.country_code; the IS NULL filter keeps the retry from rewriting anything.
-- 2. order_review.delivery_rating / packaging_rating and order_item_review.rating / fit_rating /
--    recommend are NOT NULL there (recommend DEFAULT 1); 0045 declares them NULL DEFAULT NULL and
--    review.go inserts NULLs. Re-declared with the 0045 types, per table, only while one is NOT NULL.
-- 3. order_item FK indexes are named product_id / size_id there instead of fk_order_item_product /
--    fk_order_item_size: renamed only where the old name exists and the new one does not.
--
-- One PREPARE/EXECUTE/DEALLOCATE per line (production runs without multiStatements). Strings are
-- single-quoted (ANSI_QUOTES on DO MySQL).

-- 1a. address.country_code
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'address' AND COLUMN_NAME = 'country_code');
SET @sql := IF(@need,
    'ALTER TABLE address ADD COLUMN country_code CHAR(2) NULL DEFAULT NULL COMMENT ''ISO 3166-1 alpha-2 code derived from country string'' AFTER country',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 1b. backfill as 0053, only while idx_address_country_code is still missing
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'address' AND INDEX_NAME = 'idx_address_country_code');
SET @sql := IF(@need,
    'UPDATE address SET country_code = UPPER(country) WHERE country_code IS NULL AND LENGTH(country) = 2 AND country != ''string''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 1c. idx_address_country_code
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'address' AND INDEX_NAME = 'idx_address_country_code');
SET @sql := IF(@need, 'CREATE INDEX idx_address_country_code ON address (country_code)', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- migrationlint:allow-retroactive the ENUM lists are exactly 0045's; the MODIFY runs only on the drifted databases, whose review tables are empty (checked 25.11), and it only drops NOT NULL
-- 2a. order_review ratings back to NULL DEFAULT NULL (0045)
SET @need := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_review'
      AND COLUMN_NAME IN ('delivery_rating', 'packaging_rating') AND IS_NULLABLE = 'NO');
SET @sql := IF(@need,
    'ALTER TABLE order_review MODIFY COLUMN delivery_rating ENUM(''much_faster_than_expected'', ''faster_than_expected'', ''as_expected'', ''slower_than_expected'', ''much_slower_than_expected'') NULL DEFAULT NULL, MODIFY COLUMN packaging_rating ENUM(''damaged'', ''acceptable'', ''good'', ''excellent'') NULL DEFAULT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 2b. order_item_review ratings back to NULL DEFAULT NULL (0045)
SET @need := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item_review'
      AND COLUMN_NAME IN ('rating', 'fit_rating', 'recommend') AND IS_NULLABLE = 'NO');
SET @sql := IF(@need,
    'ALTER TABLE order_item_review MODIFY COLUMN rating ENUM(''poor'', ''fair'', ''good'', ''very_good'', ''excellent'') NULL DEFAULT NULL, MODIFY COLUMN fit_rating ENUM(''runs_small'', ''slightly_small'', ''true_to_size'', ''slightly_large'', ''runs_large'') NULL DEFAULT NULL, MODIFY COLUMN recommend BOOLEAN NULL DEFAULT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 3a. order_item index product_id -> fk_order_item_product
SET @need := (SELECT COUNT(DISTINCT INDEX_NAME) = 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item' AND INDEX_NAME IN ('product_id', 'fk_order_item_product'))
    AND (SELECT COUNT(*) > 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item' AND INDEX_NAME = 'product_id');
SET @sql := IF(@need, 'ALTER TABLE order_item RENAME INDEX product_id TO fk_order_item_product', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 3b. order_item index size_id -> fk_order_item_size
SET @need := (SELECT COUNT(DISTINCT INDEX_NAME) = 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item' AND INDEX_NAME IN ('size_id', 'fk_order_item_size'))
    AND (SELECT COUNT(*) > 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item' AND INDEX_NAME = 'size_id');
SET @sql := IF(@need, 'ALTER TABLE order_item RENAME INDEX size_id TO fk_order_item_size', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
-- No-op on purpose: dropping country_code or re-tightening the ratings would recreate the drift this
-- file repairs (and NOT NULL would fail on rows with NULL ratings). The index names stay too.
SELECT 1;

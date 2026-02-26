-- +migrate Up
-- Migration: Replace auto-generated SKU with application-managed immutable SKU
-- Purpose: GENERATED ALWAYS columns recompute on source column updates, silently mutating the SKU.
--          Moving to app-managed SKU ensures immutability and human-readable category codes.
-- New format: CAT(3)-COL(3)-G(1)ID  e.g. TSH-BLK-M34

-- Drop existing sku index and column (ignore errors if already dropped)
SET @sql = IF(
    (SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'product' AND index_name = 'sku') > 0,
    'ALTER TABLE product DROP INDEX sku',
    'SELECT 1'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @sql = IF(
    (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'product' AND column_name = 'sku') > 0,
    'ALTER TABLE product DROP COLUMN sku',
    'SELECT 1'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Add new sku column as regular VARCHAR
ALTER TABLE product ADD COLUMN sku VARCHAR(20) NOT NULL DEFAULT '';

-- Backfill existing products with category names from the category table
UPDATE product p
LEFT JOIN category csub ON p.sub_category_id = csub.id
LEFT JOIN category ctop ON p.top_category_id = ctop.id
SET p.sku = CONCAT(
    UPPER(LEFT(REPLACE(COALESCE(csub.name, ctop.name, 'UNK'), '_', ''), 3)),
    '-',
    UPPER(LEFT(COALESCE(NULLIF(p.color, ''), 'UNK'), 3)),
    '-',
    CASE
        WHEN p.target_gender = 'male' THEN 'M'
        WHEN p.target_gender = 'female' THEN 'F'
        ELSE 'U'
    END,
    p.id
);

ALTER TABLE product ADD UNIQUE INDEX sku (sku);

-- +migrate Down
ALTER TABLE product DROP INDEX sku;
ALTER TABLE product DROP COLUMN sku;

ALTER TABLE product ADD COLUMN sku VARCHAR(70) GENERATED ALWAYS AS (
    CONCAT(
        UPPER(LEFT(COALESCE(collection, 'UNKN'), 4)),
        '-',
        COALESCE(top_category_id, 0), '_', COALESCE(sub_category_id, 0), '_', COALESCE(type_id, 0),
        '-',
        UPPER(LEFT(COALESCE(color, 'UNK'), 3)),
        '-',
        REGEXP_REPLACE(COALESCE(version, '0'), '[^0-9]', ''),
        '-',
        CASE WHEN target_gender = 'male' THEN 'M' WHEN target_gender = 'female' THEN 'F' ELSE 'U' END,
        '-',
        RIGHT(LPAD(CRC32(created_at), 10, '0'), 4)
    )
) STORED;

ALTER TABLE product ADD UNIQUE INDEX sku (sku);

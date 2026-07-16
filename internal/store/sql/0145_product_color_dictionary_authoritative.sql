-- +migrate Up

-- Make product.color_code the sole catalog colour identity. Legacy free-text color is retained only
-- as a denormalized dictionary name for old SQL/read models and is overwritten from color(code) on
-- every write. color_hex becomes a nullable per-colorway override; NULL means use color.hex.
--
-- Production preflight (2026-07-16): all 29 legacy product colors are covered by the 0130 mapping
-- (black/white/off_white), so UNK is a defensive migration value rather than an expected prod result.
-- Idempotent: backfills are deterministic and both ALTERs are information_schema guarded.

UPDATE product p
SET p.color_code = CASE
    -- 0144 used UNK as a temporary total backfill. Give those rows one final opportunity to resolve
    -- from their legacy value before making the FK required.
    WHEN p.color_code IS NOT NULL AND p.color_code <> 'UNK' THEN p.color_code
    ELSE COALESCE(
        (SELECT c.code FROM color c WHERE c.code = UPPER(TRIM(p.color)) LIMIT 1),
        (SELECT c.code FROM color c WHERE LOWER(c.name) = LOWER(TRIM(p.color)) LIMIT 1),
        CASE WHEN LOWER(TRIM(p.color)) IN ('off_white', 'offwhite') THEN 'OFW' ELSE NULL END,
        'UNK'
    )
END;

-- A stored hex equal to the dictionary base is not an override. Preserve genuinely distinct shades.
UPDATE product p
JOIN color c ON c.code = p.color_code
SET p.color = c.name,
    p.color_hex = CASE
        WHEN p.color_hex IS NULL OR p.color_hex = '' OR UPPER(p.color_hex) = UPPER(c.hex) THEN NULL
        ELSE p.color_hex
    END;

SET @product_color_code_nullable := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product'
      AND COLUMN_NAME = 'color_code' AND IS_NULLABLE = 'YES');
SET @sql := IF(@product_color_code_nullable > 0,
    'ALTER TABLE product MODIFY COLUMN color_code CHAR(3) NOT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @product_color_hex_required := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product'
      AND COLUMN_NAME = 'color_hex' AND IS_NULLABLE = 'NO');
SET @sql := IF(@product_color_hex_required > 0,
    'ALTER TABLE product MODIFY COLUMN color_hex VARCHAR(7) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

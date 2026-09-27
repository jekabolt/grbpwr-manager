-- T45 (27.09) — the SKU colour segment leaves the colour dictionary.
-- Owner's decisions (06-COLOURWAYS-RESEARCH.md §7), items 1, 2 and 4.
-- ---
-- Until now product.color_code (FK into the 17-colour dictionary) was at once the colour, the SKU
-- segment and the per-style uniqueness key uniq_product_style_color (style_id, color_code), 0151.
-- From here on:
--   * product.sku_color_token CHAR(3) is the SKU segment — minted by the server when the colourway
--     is created, never changed afterwards, unique per style (uniq_product_style_sku_color_token);
--   * product.color_code stays mandatory as the dictionary FAMILY tag (filters, assembly), and two
--     colourways of one style may share a family — which is why uniq_product_style_color goes.
-- ---
-- STEPS, each re-runnable (MySQL auto-commits DDL, a failed apply re-runs this file from the top):
--   1. add the column, NULLABLE. An older binary (a rollback) inserts products without naming it,
--      and a NOT NULL column without a default would refuse every colourway it creates. Readers take
--      COALESCE(sku_color_token, color_code), so such a row still reads the same token it minted.
--   2. backfill the token from color_code. The SKUs of existing colourways therefore do not move.
--      updated_at is assigned to itself so ON UPDATE CURRENT_TIMESTAMP does not stamp every product
--      as edited today (the storefront uses it for freshness).
--   3. add the unique on (style_id, sku_color_token) BEFORE dropping the old one. Step 2 copies a
--      column that uniq_product_style_color kept unique per style, so this index cannot meet a
--      duplicate; and it can serve the style_id foreign key if nothing else did.
--   4. drop uniq_product_style_color.
-- NULL tokens (rollback rows) do not collide in a MySQL unique index; the application pins them.
-- No CHECK constraint. PREPARE / EXECUTE / DEALLOCATE one per line (no multiStatements on prod).

-- +migrate Up

SET @t45_has_token := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'sku_color_token');
SET @ddl := IF(@t45_has_token = 0,
    'ALTER TABLE product ADD COLUMN sku_color_token CHAR(3) NULL COMMENT ''SKU colour segment, minted on create, immutable (T45)''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

UPDATE product SET sku_color_token = color_code, updated_at = updated_at WHERE sku_color_token IS NULL;

SET @t45_has_token_uniq := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND INDEX_NAME = 'uniq_product_style_sku_color_token');
SET @ddl := IF(@t45_has_token_uniq = 0,
    'ALTER TABLE product ADD CONSTRAINT uniq_product_style_sku_color_token UNIQUE (style_id, sku_color_token)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @t45_has_colour_uniq := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND INDEX_NAME = 'uniq_product_style_color');
SET @ddl := IF(@t45_has_colour_uniq > 0,
    'ALTER TABLE product DROP INDEX uniq_product_style_color',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
-- The old unique comes back only when no style holds two colourways of one family — after T45 that
-- is legal data, and a Down must not fail on it. Then the style_id foreign key keeps an index
-- (idx_product_style_id normally serves it), the token unique goes, then the column.

SET @t45_family_dupes := (SELECT COUNT(*) FROM (
    SELECT style_id FROM product GROUP BY style_id, color_code HAVING COUNT(*) > 1) d);
SET @t45_has_colour_uniq_down := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND INDEX_NAME = 'uniq_product_style_color');
SET @ddl := IF(@t45_has_colour_uniq_down = 0 AND @t45_family_dupes = 0,
    'ALTER TABLE product ADD CONSTRAINT uniq_product_style_color UNIQUE (style_id, color_code)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @t45_style_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product'
      AND INDEX_NAME <> 'uniq_product_style_sku_color_token'
      AND SEQ_IN_INDEX = 1 AND COLUMN_NAME = 'style_id');
SET @ddl := IF(@t45_style_idx = 0,
    'ALTER TABLE product ADD INDEX idx_product_style_id (style_id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @t45_has_token_uniq_down := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND INDEX_NAME = 'uniq_product_style_sku_color_token');
SET @ddl := IF(@t45_has_token_uniq_down > 0,
    'ALTER TABLE product DROP INDEX uniq_product_style_sku_color_token',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @t45_has_token_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'sku_color_token');
SET @ddl := IF(@t45_has_token_down > 0,
    'ALTER TABLE product DROP COLUMN sku_color_token',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

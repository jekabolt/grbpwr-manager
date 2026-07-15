-- +migrate Up

-- SKU redesign task 01: a controlled colour dictionary. The colour segment of the new SKU
-- (SS26-00021-BLK) must be deterministic and collision-free, so colour codes come from a
-- dictionary of exactly-3-char unique codes instead of free-text. product.color_code and
-- tech_card_colorway.color_code both reference it; the base hex lives on the dictionary while
-- product.color_hex stays as an OPTIONAL per-product shade override (prod has three different
-- hexes for "black" — deliberate variance we keep).
--
-- The free-text product.color column is intentionally NOT dropped here: it still feeds the
-- storefront colour name and the backfill's free-text->code mapper reads it. Its removal is a
-- PR2 (merch-dictionary) cleanup once color_code is populated everywhere. UNK is seeded so the
-- generator's last-resort colour fallback is FK-valid.
--
-- Idempotent: table create is guarded with IF NOT EXISTS; guarded ADD COLUMN / ADD FK via information_schema
-- (multi-line PREPARE/EXECUTE/DEALLOCATE — a single line trips 1064 on the managed DSN, see 0124),
-- seed via INSERT IGNORE, data backfill only fills still-NULL codes. Constraint named explicitly.

CREATE TABLE IF NOT EXISTS color (
    id         INT PRIMARY KEY AUTO_INCREMENT,
    code       CHAR(3)     NOT NULL,
    name       VARCHAR(64) NOT NULL,
    hex        CHAR(7)     NULL,
    created_at TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT uniq_color_code UNIQUE (code),
    CONSTRAINT color_code_len3 CHECK (CHAR_LENGTH(code) = 3)
);

-- Canonical starting palette: the actual prod colours (black / white / off-white) plus headroom.
-- UNK is the FK-valid target for the generator's last-resort fallback.
INSERT IGNORE INTO color (code, name, hex) VALUES
    ('BLK', 'black',     '#000000'),
    ('WHT', 'white',     '#FFFFFF'),
    ('OFW', 'off-white', '#F5F5F0'),
    ('GRY', 'grey',      '#808080'),
    ('NAV', 'navy',      '#1A2238'),
    ('BLU', 'blue',      '#2A4B8D'),
    ('RED', 'red',       '#B00020'),
    ('GRN', 'green',     '#2E7D32'),
    ('BRN', 'brown',     '#5D4037'),
    ('BEI', 'beige',     '#D8CAB0'),
    ('PNK', 'pink',      '#E59BB0'),
    ('YLW', 'yellow',    '#F2C14E'),
    ('ORG', 'orange',    '#E08A3C'),
    ('PRP', 'purple',    '#6A4C93'),
    ('SLV', 'silver',    '#C0C0C0'),
    ('GLD', 'gold',      '#C9A227'),
    ('UNK', 'unknown',   NULL);

-- product.color_code (nullable; FK -> color.code, RESTRICT so an in-use colour can't be deleted).
SET @need_pc := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'color_code');
SET @sql := IF(@need_pc,
    'ALTER TABLE product ADD COLUMN color_code CHAR(3) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_pc_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'product'
      AND CONSTRAINT_NAME = 'fk_product_color_code');
SET @sql := IF(@need_pc_fk,
    'ALTER TABLE product ADD CONSTRAINT fk_product_color_code FOREIGN KEY (color_code) REFERENCES color(code) ON DELETE RESTRICT ON UPDATE CASCADE',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- tech_card_colorway.color_code (nullable; same FK behaviour).
SET @need_cw := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_colorway' AND COLUMN_NAME = 'color_code');
SET @sql := IF(@need_cw,
    'ALTER TABLE tech_card_colorway ADD COLUMN color_code CHAR(3) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_cw_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_colorway'
      AND CONSTRAINT_NAME = 'fk_colorway_color_code');
SET @sql := IF(@need_cw_fk,
    'ALTER TABLE tech_card_colorway ADD CONSTRAINT fk_colorway_color_code FOREIGN KEY (color_code) REFERENCES color(code) ON DELETE RESTRICT ON UPDATE CASCADE',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Backfill product.color_code from the free-text product.color, matching the prod values. Only
-- rows still NULL are touched, so a re-run is a no-op. Anything unmatched stays NULL and the
-- Go boot-backfill / generator applies the name-translit or UNK fallback.
UPDATE product SET color_code = 'BLK' WHERE color_code IS NULL AND LOWER(color) IN ('black', 'blk');
UPDATE product SET color_code = 'WHT' WHERE color_code IS NULL AND LOWER(color) IN ('white', 'wht');
UPDATE product SET color_code = 'OFW' WHERE color_code IS NULL AND LOWER(color) IN ('off_white', 'off-white', 'offwhite', 'ofw');
UPDATE product SET color_code = 'GRY' WHERE color_code IS NULL AND LOWER(color) IN ('grey', 'gray', 'gry');
UPDATE product SET color_code = 'NAV' WHERE color_code IS NULL AND LOWER(color) IN ('navy', 'nav');
UPDATE product SET color_code = 'BLU' WHERE color_code IS NULL AND LOWER(color) IN ('blue', 'blu');
UPDATE product SET color_code = 'RED' WHERE color_code IS NULL AND LOWER(color) = 'red';
UPDATE product SET color_code = 'GRN' WHERE color_code IS NULL AND LOWER(color) IN ('green', 'grn');
UPDATE product SET color_code = 'BRN' WHERE color_code IS NULL AND LOWER(color) IN ('brown', 'brn');
UPDATE product SET color_code = 'BEI' WHERE color_code IS NULL AND LOWER(color) IN ('beige', 'bei');
UPDATE product SET color_code = 'PNK' WHERE color_code IS NULL AND LOWER(color) IN ('pink', 'pnk');

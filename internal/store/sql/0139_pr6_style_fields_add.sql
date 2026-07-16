-- +migrate Up

-- PR6 phase 2 (style-level de-dup), step 1 of 3: give the STYLE (tech_card) the garment-level fields
-- that are invariant across a style's colourways. A style is one pattern/design; its colourways differ
-- only by colour. So brand/season/collection/target_gender/fit/composition/care/model_wears and the
-- category taxonomy (top/sub/type) belong to the STYLE, not to each colourway (product) — today they
-- are duplicated onto every product. This step is purely ADDITIVE: it adds the missing columns to
-- tech_card and backfills them from the representative linked product (today's source of truth).
-- Nothing reads them yet, so the app behaves identically; the next steps switch product's read/write
-- to the style and drop the duplicated product columns.
--
-- brand/season/collection/target_gender already exist on tech_card and are OVERWRITTEN from the product
-- so the style reflects exactly what the catalogue showed (the storefront reads product today; when the
-- read flips to the style, displayed values must not change). The other fields are freshly added.
--
-- Representative product = MIN(product.id) per style_id. These fields are invariant across a style's
-- colourways, so any linked colourway agrees; MIN is just a deterministic pick. Styles with no product
-- (pure dev tech cards) keep NULLs — no colourway reads them. season_code/season_year (PR1 SKU parts)
-- are intentionally NOT touched: they are a separate normalized concern used only by the SKU generator.
-- care_instructions is added WITHOUT the product's belt-and-suspenders regex CHECK — validation is
-- enforced in the write path (which moves to the style in step 3); backfilled values already passed it.
--
-- Idempotent: guarded ADD COLUMN / ADD FK via information_schema (multi-line PREPARE/EXECUTE/DEALLOCATE
-- — a single line trips 1064 on the managed DSN, see 0124); the backfill overwrites deterministically,
-- so a re-run yields the same result.

-- 1) Columns (guard on top_category_id — all added together).
SET @need_cols := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'top_category_id');
SET @sql := IF(@need_cols,
    'ALTER TABLE tech_card
        ADD COLUMN fit                   VARCHAR(50)  NULL,
        ADD COLUMN composition           JSON         NULL,
        ADD COLUMN care_instructions     VARCHAR(255) NULL,
        ADD COLUMN model_wears_height_cm INT          NULL,
        ADD COLUMN model_wears_size_id   INT          NULL,
        ADD COLUMN top_category_id       INT          NULL,
        ADD COLUMN sub_category_id       INT          NULL,
        ADD COLUMN type_id               INT          NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 2) Foreign keys (each guarded on its own so a partial prior run self-heals).
SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND CONSTRAINT_NAME = 'fk_tech_card_model_wears_size');
SET @sql := IF(@need_fk,
    'ALTER TABLE tech_card ADD CONSTRAINT fk_tech_card_model_wears_size
        FOREIGN KEY (model_wears_size_id) REFERENCES size(id)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND CONSTRAINT_NAME = 'fk_tech_card_top_category');
SET @sql := IF(@need_fk,
    'ALTER TABLE tech_card ADD CONSTRAINT fk_tech_card_top_category
        FOREIGN KEY (top_category_id) REFERENCES category(id)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND CONSTRAINT_NAME = 'fk_tech_card_sub_category');
SET @sql := IF(@need_fk,
    'ALTER TABLE tech_card ADD CONSTRAINT fk_tech_card_sub_category
        FOREIGN KEY (sub_category_id) REFERENCES category(id)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND CONSTRAINT_NAME = 'fk_tech_card_type_category');
SET @sql := IF(@need_fk,
    'ALTER TABLE tech_card ADD CONSTRAINT fk_tech_card_type_category
        FOREIGN KEY (type_id) REFERENCES category(id)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 3) Backfill from the representative product (MIN id per style). Overwrites deterministically.
-- NOTE on season: product.season is the 2-char catalogue CODE (SS/FW/PF/RC), which corresponds to the
-- style's normalized season_code (0134), NOT the free-text PLM label tech_card.season ("ss26"). So we
-- backfill season_code from product and leave tech_card.season (the PLM label, load-bearing in
-- UNIQUE(style_number, season)) untouched — clobbering it would corrupt PLM data and risk the key.
UPDATE tech_card t
JOIN (SELECT style_id, MIN(id) AS pid FROM product GROUP BY style_id) rep ON rep.style_id = t.id
JOIN product p ON p.id = rep.pid
SET
    t.brand                 = p.brand,
    t.season_code           = p.season,
    t.collection            = p.collection,
    t.target_gender         = p.target_gender,
    t.fit                   = p.fit,
    t.composition           = p.composition,
    t.care_instructions     = p.care_instructions,
    t.model_wears_height_cm = p.model_wears_height_cm,
    t.model_wears_size_id   = p.model_wears_size_id,
    t.top_category_id       = p.top_category_id,
    t.sub_category_id       = p.sub_category_id,
    t.type_id               = p.type_id;

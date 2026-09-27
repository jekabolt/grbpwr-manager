-- T45 (27.09) — two colourways of one style may share a colour family. SECOND PUSH of two (D-69).
-- ---
-- 0376 (the first push) made product.sku_color_token the SKU colour segment, unique per style
-- (uniq_product_style_sku_color_token), and deliberately KEPT uniq_product_style_color
-- (style_id, color_code), 0151. Until this file runs, a second colourway of one family in a style is
-- refused by that index and answered as the ordinary «a colourway with this colour already exists»
-- (FailedPrecondition). This file drops it: from here on color_code is only the dictionary family —
-- a filter tag several colourways of one style may share — and only the token is unique.
-- ---
-- ⚠ AFTER THIS MIGRATION THE BINARY MUST NOT BE ROLLED BACK PAST T45. An older binary re-derives an
-- unfrozen SKU's colour segment from color_code: with two colourways of one family in a style it
-- mints the SAME segment for both (…-BLK twice), and for any colourway whose token is not its
-- family (BKW) it rewrites the SKU away from the token. Once an order or a label freezes such a SKU,
-- the immutable token and the frozen SKU disagree for good. Roll forward instead. (The schema Down
-- below refuses while any style holds two colourways of one family, so it cannot quietly bring the
-- old invariant back over data that breaks it.)
-- ---
-- Ship it ALONE, after 0375 + 0376 + the T45 binary are live and confirmed ACTIVE: the push that
-- carries this file is the point of no return for binary rollback, and it should carry nothing else.
-- One gated statement group, re-runnable (information_schema gate). The style_id foreign key keeps
-- its index throughout (idx_product_style_id, 0138; the token unique also leads with style_id).
-- No CHECK. PREPARE / EXECUTE / DEALLOCATE one per line (no multiStatements on prod).

-- +migrate Up

SET @t45_has_colour_uniq := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND INDEX_NAME = 'uniq_product_style_color');
SET @ddl := IF(@t45_has_colour_uniq > 0,
    'ALTER TABLE product DROP INDEX uniq_product_style_color',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
-- The family unique comes back only over data it can hold. THE REFUSAL COMES FIRST, BEFORE ANY DDL
-- (precedent 0273 / 0287 Down): a style holding two colourways of one family — legal after this
-- migration — would make the ADD CONSTRAINT fail half-way through a Down or, skipped, leave a schema
-- the pre-0377 code does not expect. SIGNAL cannot be prepared, so the refusal is a SELECT of a
-- column whose NAME is the message: ERROR 1054, nothing changed, sql-migrate keeps its gorp row.
-- What to do when it fires: re-family, or delete, all but one colourway per style and family
-- (archived ones count — they hold rows), then run the Down again.

SET @t45_family_dupes := (SELECT COUNT(*) FROM (
    SELECT 1 FROM product GROUP BY style_id, color_code HAVING COUNT(*) > 1) d);
SET @ddl := IF(@t45_family_dupes = 0, 'SELECT 1',
    CONCAT('SELECT `0377 Down blocked: ', @t45_family_dupes, ' families are shared within a style`'));
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @t45_has_colour_uniq_down := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND INDEX_NAME = 'uniq_product_style_color');
SET @ddl := IF(@t45_has_colour_uniq_down = 0,
    'ALTER TABLE product ADD CONSTRAINT uniq_product_style_color UNIQUE (style_id, color_code)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

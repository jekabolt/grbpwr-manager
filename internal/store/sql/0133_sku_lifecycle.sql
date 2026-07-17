-- +migrate Up

-- SKU redesign task 04: schema for the new SKU. Widen product.sku to hold the new base/variant
-- format, add product.sku_locked_at (freeze marker, set at first sale/label — see task 07), and add
-- product_size.sku as a first-class per-size variant SKU with its own UNIQUE index.
--
-- CRITICAL (plan review 70 §3.1): product_size.sku is added as NULL, NOT `NOT NULL DEFAULT ''`. The
-- variant SKUs are minted by a Go boot-backfill (task 13) that runs AFTER migrations; with a
-- DEFAULT '' every existing product_size row would take '' and the UNIQUE index in this same
-- migration would fail with a duplicate-entry error, halting prod startup. MySQL UNIQUE ignores
-- NULLs, so NULL = "not minted yet" and uniqueness holds from the first minted value.
--
-- Also adds the STRUCTURAL collision guard (review 70 §3.6): UNIQUE(tech_card_id, color_code) on
-- tech_card_colorway. Two colourways of one style sharing a colour is the only real path to a base
-- SKU collision; forbidding it structurally lets the generator keep fixed lengths without an
-- emergency numeric suffix in practice. color_code is NULL until an operator picks it, and MySQL
-- treats multiple NULLs as distinct, so this adds cleanly over current data.
--
-- Idempotent: guarded MODIFY/ADD/UNIQUE via information_schema (multi-line PREPARE/EXECUTE/DEALLOCATE
-- — a single line trips 1064 on the managed DSN, see 0124).

-- 1) Widen product.sku 30 -> 64 (its existing UNIQUE index `sku` is preserved by MODIFY). Guarded on
-- current length so a re-run does not rebuild the table.
SET @need_widen := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'sku'
      AND CHARACTER_MAXIMUM_LENGTH < 64);
SET @sql := IF(@need_widen,
    'ALTER TABLE product MODIFY COLUMN sku VARCHAR(64) NOT NULL DEFAULT ''''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 2) product.sku_locked_at freeze marker.
SET @need_lock := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'sku_locked_at');
SET @sql := IF(@need_lock,
    'ALTER TABLE product ADD COLUMN sku_locked_at TIMESTAMP NULL DEFAULT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 3) product_size.sku (NULL — see the CRITICAL note above).
SET @need_ps := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product_size' AND COLUMN_NAME = 'sku');
SET @sql := IF(@need_ps,
    'ALTER TABLE product_size ADD COLUMN sku VARCHAR(64) NULL DEFAULT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_ps_uq := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product_size' AND INDEX_NAME = 'uniq_product_size_sku');
SET @sql := IF(@need_ps_uq,
    'ALTER TABLE product_size ADD CONSTRAINT uniq_product_size_sku UNIQUE (sku)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 4) Structural base-SKU collision guard on colourways.
SET @need_cw_uq := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_colorway' AND INDEX_NAME = 'uniq_colorway_card_color');
SET @sql := IF(@need_cw_uq,
    'ALTER TABLE tech_card_colorway ADD CONSTRAINT uniq_colorway_card_color UNIQUE (tech_card_id, color_code)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

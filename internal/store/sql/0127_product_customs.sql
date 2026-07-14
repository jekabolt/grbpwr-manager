-- +migrate Up

-- Per-product customs data for international shipping labels (Sendcloud). Set by an operator once
-- per product; GenerateShippingLabel folds it into the parcel_items customs declaration for
-- cross-border (non-intra-EU) shipments. All NULL by default — a product without customs data ships
-- domestically/intra-EU fine, and the label call errors clearly if customs is required but missing.
--   hs_code             — Harmonized System tariff code
--   customs_description — declared goods description (falls back to SKU when empty)
--
-- NOTE: product.country_of_origin already exists (VARCHAR(50) NOT NULL, since 0001) — it is the
-- product's manufacture country and is REUSED as the Sendcloud origin_country (resolved to ISO-2 at
-- label build time). We therefore add ONLY hs_code and customs_description here; adding
-- country_of_origin again would collide (Error 1060 duplicate column).
--
-- Idempotent: guarded ADD COLUMN via information_schema (see 0124). Both new columns are added in one
-- atomic ALTER, guarded on hs_code, so a re-run after a completed apply is a no-op.

SET @need_cols := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'hs_code');
SET @sql := IF(@need_cols,
    'ALTER TABLE product
        ADD COLUMN hs_code             VARCHAR(32)  NULL DEFAULT NULL,
        ADD COLUMN customs_description VARCHAR(255) NULL DEFAULT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

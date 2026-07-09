-- +migrate Up
-- Description: Add per-product cost of goods (COGS) in base currency (EUR), used to
--   compute gross margin / contribution margin in business metrics. NULL means the
--   product's unit cost is unknown, so it is excluded from margin (cost coverage < 100%).
--   Confidential: never exposed on the storefront product read path (admin-write only).
-- Affected tables: product
-- Type: additive (non-breaking)

ALTER TABLE product
  ADD COLUMN cost_price DECIMAL(10, 2) NULL DEFAULT NULL COMMENT 'Unit cost of goods (COGS) in base currency (EUR); NULL = unknown. Confidential, admin-only.';

-- +migrate Down

ALTER TABLE product
  DROP COLUMN cost_price;

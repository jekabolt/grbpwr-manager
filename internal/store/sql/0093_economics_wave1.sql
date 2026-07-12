-- +migrate Up
-- Economics audit — Wave 1: small, high-value corrections to the COGS/margin pipeline.
-- One migration accumulates all Wave-1 column additions (tasks 01,02,04,05,06,07 of the
-- tmp/plans_jul12 audit); each is a self-contained section below. Unreleased — edited in
-- place as Wave-1 tasks land, then shipped as one migration.

-- === Task 01: snapshot per-line COGS at sale time =========================================
-- Snapshot the per-unit cost of goods (COGS, base currency EUR) onto each order line at sale
-- time, mirroring the existing product_price / product_sale_percentage snapshots. Margin
-- analytics previously joined the product's CURRENT cost_price live (internal/store/metrics/
-- *.go), so re-costing a product — or re-saving its tech card, which seeds product.cost_price
-- — retroactively rewrote the margin of every past order. Reading this snapshot (with a live
-- fallback to product.cost_price for rows that predate the column) makes historical COGS
-- reproducible: a re-cost only affects orders placed afterwards.
ALTER TABLE order_item
  ADD COLUMN cost_price_at_sale DECIMAL(10, 2) NULL
    COMMENT 'per-unit COGS (base currency EUR) snapshotted at sale; NULL = unknown, metrics fall back to product.cost_price';

-- Backfill existing lines with the product's current cost. This is exactly the value the
-- margin queries used before this column existed, so history does not change on deploy; from
-- the next order onward each line captures its point-in-time cost instead.
UPDATE order_item oi
  JOIN product p ON p.id = oi.product_id
  SET oi.cost_price_at_sale = p.cost_price
  WHERE p.cost_price IS NOT NULL;

-- +migrate Down
-- Reverse Wave-1 sections in reverse order.

-- Task 01
ALTER TABLE order_item
  DROP COLUMN cost_price_at_sale;

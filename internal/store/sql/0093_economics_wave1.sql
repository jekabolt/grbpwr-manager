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

-- === Task 02: cost_price provenance + deterministic primary tech card ======================
-- product.cost_price had two silently-racing writers (manual admin input vs the tech-card
-- seed) with no provenance and last-write-wins across multiple linked cards. Add:
--   * cost_price_source / cost_price_tech_card_id / cost_price_updated_at — where the stored
--     cost came from and when, so a manual value is never silently clobbered by a seed.
--   * primary_tech_card_id — the one card authoritative for seeding this product's cost.
--     It lives on product (NOT on tech_card_product, which UpdateTechCard full-replaces on
--     every save and would wipe an is_primary flag) so it is stable, and it also gives style
--     analytics a deterministic product→card link.
ALTER TABLE product
  ADD COLUMN cost_price_source VARCHAR(16) NULL
    COMMENT 'provenance of cost_price: manual | tech_card (NULL = unset)',
  ADD COLUMN cost_price_tech_card_id INT NULL
    COMMENT 'FK tech_card(id): the card that seeded cost_price (NULL = manual/unset)',
  ADD COLUMN cost_price_updated_at DATETIME NULL
    COMMENT 'when cost_price was last written (UTC)',
  ADD COLUMN primary_tech_card_id INT NULL
    COMMENT 'FK tech_card(id): authoritative card for seeding this product cost; stable across tech-card full-replace',
  ADD CONSTRAINT fk_product_cost_tech_card FOREIGN KEY (cost_price_tech_card_id) REFERENCES tech_card(id) ON DELETE SET NULL,
  ADD CONSTRAINT fk_product_primary_tech_card FOREIGN KEY (primary_tech_card_id) REFERENCES tech_card(id) ON DELETE SET NULL;

-- Backfill: every product linked to a tech card gets its lowest-id linked card as primary
-- (deterministic; the old seed touched all linked products, so making one primary preserves
-- coverage). A product that already has a cost is attributed to that card as 'tech_card'
-- provenance (the pre-existing seed behaviour clobbered linked products' costs anyway, so
-- this is not a regression); operators can re-set a value to mark it 'manual' afterwards.
UPDATE product p
  JOIN (SELECT product_id, MIN(tech_card_id) AS tc FROM tech_card_product GROUP BY product_id) t
    ON t.product_id = p.id
  SET p.primary_tech_card_id = t.tc,
      p.cost_price_source       = CASE WHEN p.cost_price IS NOT NULL THEN 'tech_card' ELSE p.cost_price_source END,
      p.cost_price_tech_card_id = CASE WHEN p.cost_price IS NOT NULL THEN t.tc ELSE p.cost_price_tech_card_id END,
      p.cost_price_updated_at   = CASE WHEN p.cost_price IS NOT NULL THEN NOW() ELSE p.cost_price_updated_at END;

-- Products with a cost but no tech-card link → manual provenance.
UPDATE product p
  SET p.cost_price_source = 'manual', p.cost_price_updated_at = NOW()
  WHERE p.cost_price IS NOT NULL AND p.cost_price_source IS NULL;

-- === Task 04: manual FX rates for costing =================================================
-- The shop has no live FX (currency_rate was dropped in 0053), but a tech card's costing is
-- genuinely multi-currency: fabric bought in USD/CNY, CMT billed in another currency. Before
-- this, a costing not in the base currency was silently skipped by the product-cost seed, and
-- a BOM line in a foreign currency was excluded from unit_cost — so unit_cost was undercounted
-- and such products fell out of margin analytics entirely. This table holds a rarely-changed,
-- operator-entered rate per currency (how many base-currency units one unit is worth), letting
-- the costing fold every currency into a base (*_base) unit cost that the seed can use.
CREATE TABLE IF NOT EXISTS costing_fx_rate (
  currency CHAR(3) NOT NULL COMMENT 'ISO 4217, uppercase',
  rate_to_base DECIMAL(18, 8) NOT NULL COMMENT 'how many base-currency units 1 of this currency is worth'
    CHECK (rate_to_base > 0),
  valid_from DATE NOT NULL COMMENT 'the rate applies from this date; the latest <= today is used',
  PRIMARY KEY (currency, valid_from)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Manual FX rates to fold multi-currency costing into base currency';

-- === Task 05: per-method payment fee model =================================================
-- Payment fees were captured only for Stripe (customer_order.payment_fee from the balance
-- transaction). bank-invoice / cash / non-EUR-settled / pre-feature orders contributed a €0
-- fee, so contribution margin was systematically overstated for them. Give each payment
-- method a fee model (percent + fixed) so the fee of an order without a captured Stripe fee
-- can be estimated. Default 0 → no behaviour change until an operator sets a method's fees.
ALTER TABLE payment_method
  ADD COLUMN fee_pct DECIMAL(5, 2) NOT NULL DEFAULT 0 COMMENT 'estimated processing fee as %% of the order total',
  ADD COLUMN fee_fixed DECIMAL(10, 2) NOT NULL DEFAULT 0 COMMENT 'estimated fixed processing fee per order, base currency';

-- === Task 06: actual shipping cost ========================================================
-- Margin used the customer-charged carrier price as the shipping "cost" proxy, so shipping
-- revenue and cost largely cancelled and real logistics (incl. subsidised/free shipping and
-- return legs) were invisible. Record the actual carrier invoice and the return-leg cost so
-- contribution margin can use them (falling back to the charged price when not entered).
ALTER TABLE shipment
  ADD COLUMN actual_cost DECIMAL(10, 2) NULL COMMENT 'actual carrier invoice for this shipment, base currency (NULL = not entered)',
  ADD COLUMN return_shipping_cost DECIMAL(10, 2) NULL COMMENT 'reverse-logistics cost of a return, base currency (NULL = none)';

-- === Task 07: snapshot base-currency line price ===========================================
-- Fallback revenue (orders without total_settled_base) reconstructs from the CURRENT base
-- price × the snapshotted sale%, so changing a product's price rewrote historical revenue for
-- those orders. Snapshot the base-currency (EUR) list price onto each line at sale, mirroring
-- product_price / product_sale_percentage, so history is stable; metrics fall back to the live
-- base price for rows placed before the column existed.
ALTER TABLE order_item
  ADD COLUMN product_price_base DECIMAL(10, 2) NULL COMMENT 'base-currency (EUR) list price snapshotted at sale; NULL = unknown, metrics fall back to product_price';

-- Backfill with today's base price (EUR is the base currency). This is the value metrics used
-- before the column existed, so no history change on deploy.
UPDATE order_item oi
  JOIN product_price pp ON pp.product_id = oi.product_id AND UPPER(pp.currency) = 'EUR'
  SET oi.product_price_base = pp.price;

-- +migrate Down
-- Reverse Wave-1 sections in reverse order.

-- Task 07
ALTER TABLE order_item DROP COLUMN product_price_base;

-- Task 06
ALTER TABLE shipment
  DROP COLUMN actual_cost,
  DROP COLUMN return_shipping_cost;

-- Task 05
ALTER TABLE payment_method
  DROP COLUMN fee_pct,
  DROP COLUMN fee_fixed;

-- Task 04
DROP TABLE IF EXISTS costing_fx_rate;

-- Task 02
ALTER TABLE product
  DROP FOREIGN KEY fk_product_cost_tech_card,
  DROP FOREIGN KEY fk_product_primary_tech_card,
  DROP COLUMN cost_price_source,
  DROP COLUMN cost_price_tech_card_id,
  DROP COLUMN cost_price_updated_at,
  DROP COLUMN primary_tech_card_id;

-- Task 01
ALTER TABLE order_item
  DROP COLUMN cost_price_at_sale;

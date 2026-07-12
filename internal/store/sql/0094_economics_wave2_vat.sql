-- +migrate Up
-- Economics audit — Wave 2 / task 03: VAT (net-of-VAT revenue).
-- B2C prices in the EU are VAT-inclusive, but the schema had no tax column anywhere, so every
-- money metric treated gross-of-VAT customer payments as company revenue — systematically
-- overstating revenue and margin by the destination country's rate (17–27% in the EU). This
-- migration adds an operator-managed rate table and snapshots the resolved rate + VAT amount
-- onto each order; the metrics layer (phase 2) then divides revenue out to net.

-- vat_rate: the standard VAT rate per destination country (ISO 3166-1 alpha-2). One row per
-- country (the rate in force); valid_from is informational. Operators manage it via the
-- UpsertVatRates admin RPC. A destination absent from this table is treated as 0% (export /
-- non-EU) by both the order-time snapshot and the backfill.
CREATE TABLE vat_rate (
  country_code CHAR(2) NOT NULL PRIMARY KEY COMMENT 'ISO 3166-1 alpha-2 destination country, uppercase',
  rate_pct DECIMAL(5, 2) NOT NULL COMMENT 'standard VAT rate %, e.g. 21.00'
    CHECK (rate_pct >= 0 AND rate_pct < 100),
  valid_from DATE NOT NULL DEFAULT (CURRENT_DATE) COMMENT 'informational: when this rate took effect'
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Standard destination-country VAT rates for net-of-VAT revenue';

-- Seed EU-27 standard rates (2024/2025 snapshot). These are a starting point: operators MUST
-- verify and adjust them via UpsertVatRates. Rates are DESTINATION rates (EU OSS B2C); non-EU
-- destinations are intentionally absent → 0% (export).
INSERT INTO vat_rate (country_code, rate_pct) VALUES
  ('AT', 20.00), ('BE', 21.00), ('BG', 20.00), ('HR', 25.00), ('CY', 19.00), ('CZ', 21.00),
  ('DK', 25.00), ('EE', 22.00), ('FI', 25.50), ('FR', 20.00), ('DE', 19.00), ('GR', 24.00),
  ('HU', 27.00), ('IE', 23.00), ('IT', 22.00), ('LV', 21.00), ('LT', 21.00), ('LU', 17.00),
  ('MT', 18.00), ('NL', 21.00), ('PL', 23.00), ('PT', 23.00), ('RO', 19.00), ('SK', 23.00),
  ('SI', 22.00), ('ES', 21.00), ('SE', 25.00);

-- Snapshot the resolved rate + VAT amount (order currency) onto each order at sale, mirroring
-- the price/discount snapshots so historical net revenue is reproducible if a rate later
-- changes. Inclusive pricing: vat_amount = total_price × rate / (100 + rate).
ALTER TABLE customer_order
  ADD COLUMN vat_rate_pct DECIMAL(5, 2) NULL
    COMMENT 'destination VAT rate % snapshotted at order (NULL = pre-feature; metrics treat as 0)',
  ADD COLUMN vat_amount DECIMAL(10, 2) NULL
    COMMENT 'VAT included in total_price, order currency (inclusive: total×rate/(100+rate))';

-- Backfill existing orders from the seeded rates by shipping-address country (address.country
-- is the ISO alpha-2 code — see migration 0053). Approximation: VAT rates change rarely, so
-- today's rate is the best available estimate for history. Orders shipping outside the table
-- (export / unknown country) get 0. An order with no buyer/address row keeps NULL (→ 0 in
-- metrics). This is the same value the metrics will use, applied once so history is stable.
UPDATE customer_order co
  JOIN buyer b ON b.order_id = co.id
  JOIN address a ON a.id = b.shipping_address_id
  LEFT JOIN vat_rate v ON v.country_code = UPPER(a.country)
  SET co.vat_rate_pct = COALESCE(v.rate_pct, 0),
      co.vat_amount = ROUND(co.total_price * COALESCE(v.rate_pct, 0) / (100 + COALESCE(v.rate_pct, 0)), 2);

-- +migrate Down
ALTER TABLE customer_order
  DROP COLUMN vat_rate_pct,
  DROP COLUMN vat_amount;

DROP TABLE IF EXISTS vat_rate;

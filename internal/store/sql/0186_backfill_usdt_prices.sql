-- +migrate Up
-- USDT (a USD stablecoin) became a REQUIRED price/accounting currency (internal/currency.
-- requiredCurrencies): there is no FX API, so every shipment carrier, the complimentary-shipping
-- threshold, and every ACTIVE/HIDDEN colourway must carry an explicit USDT price or the
-- "missing required currency" gate fails. The completeness gate sits on the colourway ->ACTIVE edge
-- (publish / unhide, store/product/lifecycle.go: checkColorwayRequiredCurrencies) and on shipping-price
-- writes (apisrv/admin/shipment.go). Colourways/carriers created before USDT became required have no
-- USDT row, so the next unhide (HIDDEN->ACTIVE) — or, for a live colourway, a hide-then-unhide — would
-- fail the gate. Backfill a starting USDT row (mirroring the existing EUR amount as a placeholder;
-- merchants re-price USDT manually afterwards) for every row that has EUR but not yet USDT.
--
-- Runs AFTER 0185 (which widened the currency columns to hold the 4-char 'USDT'). Same pattern as PLN's
-- 0181 (shipping) / 0182 (product), consolidated here.
--
-- Idempotent / crash-safe: INSERT ... SELECT ... WHERE NOT EXISTS (backed by the UNIQUE keys —
-- product_price(product_id,currency), shipment_carrier_price(shipment_carrier_id,currency),
-- complimentary_shipping_price(currency)) is a no-op on rerun once the USDT rows exist, so a mid-file
-- crash and the next-boot re-run are both safe. All string literals are single-quoted (ANSI_QUOTES-safe
-- on the managed cluster — double quotes would parse as identifiers).

-- shipment_carrier_price: one USDT row per carrier that has EUR but not USDT (no lifecycle dimension).
INSERT INTO shipment_carrier_price (shipment_carrier_id, currency, price)
SELECT eur.shipment_carrier_id, 'USDT', eur.price
FROM shipment_carrier_price eur
WHERE eur.currency = 'EUR'
  AND NOT EXISTS (
    SELECT 1 FROM shipment_carrier_price u
    WHERE u.shipment_carrier_id = eur.shipment_carrier_id AND u.currency = 'USDT'
  );

-- complimentary_shipping_price: UNIQUE (currency) — at most one USDT row shop-wide, mirroring EUR.
INSERT INTO complimentary_shipping_price (currency, price)
SELECT 'USDT', eur.price
FROM complimentary_shipping_price eur
WHERE eur.currency = 'EUR'
  AND NOT EXISTS (
    SELECT 1 FROM complimentary_shipping_price u
    WHERE u.currency = 'USDT'
  );

-- product_price: only ACTIVE (2) or HIDDEN (3) colourways — DRAFT (1) may hold partial prices and is
-- gated only when published; ARCHIVED (4) is not activatable without first being restored to HIDDEN
-- (entity.ColorwayStatus / chk_product_lifecycle_status, 0137). Mirror EUR as the USDT placeholder.
INSERT INTO product_price (product_id, currency, price)
SELECT eur.product_id, 'USDT', eur.price
FROM product_price eur
JOIN product p ON p.id = eur.product_id
WHERE eur.currency = 'EUR'
  AND p.lifecycle_status IN (2, 3)
  AND NOT EXISTS (
    SELECT 1 FROM product_price u
    WHERE u.product_id = eur.product_id AND u.currency = 'USDT'
  );

-- +migrate Down
-- Best-effort inverse: drop the backfilled USDT rows. It cannot distinguish a backfilled placeholder
-- from a USDT price a merchant later set on the same row, so a rollback loses both; USDT only became a
-- meaningful currency with this change-set, so there is nothing from before it to preserve. DRAFT USDT
-- product prices are left intact (the Up never created them).
DELETE FROM shipment_carrier_price WHERE currency = 'USDT';
DELETE FROM complimentary_shipping_price WHERE currency = 'USDT';
DELETE u FROM product_price u
JOIN product p ON p.id = u.product_id
WHERE u.currency = 'USDT' AND p.lifecycle_status IN (2, 3);

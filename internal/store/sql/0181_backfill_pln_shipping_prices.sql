-- +migrate Up
-- PLN (Polish zloty) joins the required fiat currency set (internal/currency.requiredCurrencies):
-- there is no FX API, so every shipment carrier and the complimentary-shipping threshold must carry
-- an explicit PLN price or "missing required currency" validation fails on next edit. Backfill a
-- starting PLN row (mirroring EUR) for every existing row that has EUR but not PLN yet.
-- product_price is intentionally NOT backfilled here: merchants set product PLN prices manually.
--
-- Idempotent / crash-safe: INSERT ... SELECT ... WHERE NOT EXISTS is a no-op on rerun once the PLN
-- rows exist, so a mid-file crash and the next-boot re-run of this file are both safe. Single-quoted
-- string literals are ANSI_QUOTES-safe (double quotes would be parsed as identifiers).

-- shipment_carrier_price: PK is (id), UNIQUE (shipment_carrier_id, currency).
INSERT INTO shipment_carrier_price (shipment_carrier_id, currency, price)
SELECT eur.shipment_carrier_id, 'PLN', eur.price
FROM shipment_carrier_price eur
WHERE eur.currency = 'EUR'
  AND NOT EXISTS (
    SELECT 1 FROM shipment_carrier_price pln
    WHERE pln.shipment_carrier_id = eur.shipment_carrier_id AND pln.currency = 'PLN'
  );

-- complimentary_shipping_price: UNIQUE (currency) — at most one PLN row shop-wide, no carrier dimension.
INSERT INTO complimentary_shipping_price (currency, price)
SELECT 'PLN', eur.price
FROM complimentary_shipping_price eur
WHERE eur.currency = 'EUR'
  AND NOT EXISTS (
    SELECT 1 FROM complimentary_shipping_price pln
    WHERE pln.currency = 'PLN'
  );

-- +migrate Down
DELETE FROM shipment_carrier_price WHERE currency = 'PLN';
DELETE FROM complimentary_shipping_price WHERE currency = 'PLN';

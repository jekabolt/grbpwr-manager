-- +migrate Up
-- PLN (Polish zloty) became a REQUIRED product currency (internal/currency.requiredCurrencies, #61).
-- The completeness gate now sits on the colourway →ACTIVE edge (publish / unhide, lifecycle.go): a
-- colourway can no longer go ACTIVE without a price in every required currency. Colourways created
-- before PLN became required have no PLN product_price row, so the next unhide (HIDDEN->ACTIVE) — or,
-- for a live colourway, a hide-then-unhide — would fail the gate. Backfill a starting PLN row for every
-- ACTIVE or HIDDEN colourway that already has an EUR price but not yet a PLN one, mirroring the EUR
-- amount as a placeholder (merchants re-price PLN manually afterwards).
--
-- Scope: lifecycle_status IN (2, 3) — ACTIVE and HIDDEN only. 1=DRAFT, 2=ACTIVE, 3=HIDDEN, 4=ARCHIVED
-- (entity.ColorwayStatus / chk_product_lifecycle_status, 0137). DRAFT is intentionally excluded: a draft
-- may carry partial prices and is gated only when it is published. ARCHIVED is excluded too — it is not
-- activatable without first being restored to HIDDEN.
--
-- The sibling shipping backfill (0181) deliberately left product_price alone ("merchants set product PLN
-- prices manually"); that holds for DRAFTs, but existing NON-DRAFT colourways must stay activatable, so
-- they get the placeholder here.
--
-- Idempotent / crash-safe: INSERT ... SELECT ... WHERE NOT EXISTS (backed by UNIQUE(product_id,
-- currency)) is a no-op once the PLN rows exist, so a mid-file crash and the next-boot re-run are both
-- safe. All string literals are single-quoted (ANSI_QUOTES-safe on the managed cluster — double quotes
-- would be parsed as identifiers).

INSERT INTO product_price (product_id, currency, price)
SELECT eur.product_id, 'PLN', eur.price
FROM product_price eur
JOIN product p ON p.id = eur.product_id
WHERE eur.currency = 'EUR'
  AND p.lifecycle_status IN (2, 3)
  AND NOT EXISTS (
    SELECT 1 FROM product_price pln
    WHERE pln.product_id = eur.product_id AND pln.currency = 'PLN'
  );

-- +migrate Down
-- Best-effort inverse: drop PLN product_price rows for the ACTIVE/HIDDEN colourways the Up targeted. It
-- cannot distinguish a backfilled placeholder from a PLN price a merchant later set on the same
-- colourway, so a rollback loses both; PLN only became a meaningful product currency with this
-- change-set (#61), so there is nothing from before it to preserve. DRAFT PLN prices are left intact.
DELETE pln FROM product_price pln
JOIN product p ON p.id = pln.product_id
WHERE pln.currency = 'PLN' AND p.lifecycle_status IN (2, 3);

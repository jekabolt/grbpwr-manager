-- +migrate Up
-- Accounting phase 2, wave 1 (VAT engine) — vat_rate seed-gap fix.
--
-- The uk_stock_domestic regime resolves its rate against country GB
-- (internal/accounting/vatregime.go — RegimeRateCountry → countryGB), but the 0094 vat_rate seed
-- covers only the EU-27 and omits GB (the UK is a third country post-Brexit and is intentionally not
-- in euCountries). With no GB row the acctposting worker skips every cash / UK-stock order with
-- "vat rate missing for GB" (internal/acctposting/outbox.go) instead of posting. Seed the UK standard
-- rate (20%).
--
-- Idempotent INSERT ... SELECT ... WHERE NOT EXISTS (the 0190/0191 seed pattern): a re-run is a no-op,
-- and it will NOT clobber a rate an operator has since adjusted via UpsertVatRates.
INSERT INTO vat_rate (country_code, rate_pct)
SELECT 'GB', 20.00
WHERE NOT EXISTS (SELECT 1 FROM vat_rate v WHERE v.country_code = 'GB');

-- +migrate Down
-- seed is intentionally not removed (down on seed data is unsafe once orders reference the rate); mirrors 0190/0191.
SELECT 1;

-- +migrate Up
-- Correct the USDT currency model: USDT is EXPENSE/ACCOUNTING-ONLY and is NEVER a selling currency.
-- Feature #67 wrongly made USDT a REQUIRED selling currency, and migration 0186 backfilled placeholder
-- USDT SELLING-price rows (mirroring EUR) into the three selling-price tables — product_price,
-- shipment_carrier_price, complimentary_shipping_price — so colourways/carriers could satisfy the
-- (now-removed) "USDT required" completeness gate. Those rows are invalid under the corrected model:
-- products, colourways and carriers are never priced in USDT (internal/currency.requiredCurrencies no
-- longer lists it, and store/product.validateColorwayPrices now REJECTS a USDT product price outright).
-- Remove every USDT row from the three SELLING-price tables.
--
-- This deliberately deletes ALL currency='USDT' rows in these tables, not only the ACTIVE/HIDDEN
-- product rows 0186 created: it is a strict superset of 0186's targets, so it also cleans any stray
-- USDT product_price row left on a DRAFT colourway by the pre-fix write path (the old validateColorway
-- Prices skipped USDT via !IsStripeChargeable and stored it). Under the corrected model a USDT selling
-- price is invalid in EVERY lifecycle state, so removing them all is correct.
--
-- Sequencing / net effect: on PROD a master deploy applies 0186 (creates the USDT selling rows) then
-- 0188 (removes them) in the same boot — net zero, no USDT selling row is ever visible to serving. On
-- BETA (where 0186 already ran) 0188 cleans the existing USDT selling rows. Do NOT edit 0186 (already
-- applied on beta).
--
-- Explicitly NOT touched: customer_order.currency (a booked order is an immutable financial record —
-- never mutated by a migration; and no USDT order can be created going forward since USDT is no longer
-- IsSupported), and every EXPENSE/accounting currency column (material_price, production_run_cost,
-- tech_card_dev_expense, tech_card_bom_item, tech_card_costing, costing_fx_rate, opex_*, material_lot,
-- employee.default_currency) — USDT is now VALID there and those rows must be preserved.
--
-- Idempotent / crash-safe: a plain DELETE ... WHERE currency='USDT' deletes zero rows on re-run once
-- the USDT rows are gone, so a mid-file crash and the next-boot re-run of the whole file are both safe.
-- Prod-data-safe: only USDT SELLING rows (which must not exist under the corrected model) are removed;
-- no non-USDT data is touched. All string literals are single-quoted (ANSI_QUOTES-safe on the managed
-- cluster — double quotes would be parsed as identifiers).

DELETE FROM product_price WHERE currency = 'USDT';
DELETE FROM shipment_carrier_price WHERE currency = 'USDT';
DELETE FROM complimentary_shipping_price WHERE currency = 'USDT';

-- +migrate Down
-- Intentionally a no-op. This migration removes rows that are invalid under the corrected currency
-- model (USDT is expense-only, never a selling currency). There is nothing meaningful to restore: the
-- USDT selling rows were placeholders (0186 mirrored the EUR amount), and re-creating them would
-- re-introduce the very rows validateColorwayPrices now rejects. A deliberate rollback of the whole
-- USDT-selling change-set would run 0186's Down (which drops USDT selling rows) anyway.
SELECT 1;

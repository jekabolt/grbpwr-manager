-- +migrate Up
-- USDT joins the shop as a full PRICE/ACCOUNTING currency (internal/currency.requiredCurrencies). Its
-- code is 4 characters, but every currency-code column in the schema is CHAR(3)/VARCHAR(3), so a 'USDT'
-- write would be truncated to 'USD' under non-strict mode or hard-fail under STRICT (prod). Widen EVERY
-- currency-code column to hold 4 characters BEFORE any USDT row is written (the USDT backfill is 0186,
-- which runs after this file). This is the whole storefront/accounting surface:
--   customer_order.currency, product_price.currency, shipment_carrier_price.currency,
--   complimentary_shipping_price.currency, product_stock_change_history.(paid_currency,
--   payout_base_currency), and the tech-card / costing / OPEX / material / production-run family
--   (costing_fx_rate, material_price, material_lot, material_stock_movement, tech_card_bom_item,
--   tech_card_costing, tech_card_dev_expense, tech_card_release, production_run.planned_currency,
--   production_run_cost, opex_line, opex_recurring, employee.default_currency).
-- Two more tech-card-family columns are guarded below (tech_card.currency, tech_card_construction.
-- labour_rate_currency) even though they are not part of the live surface above: 0079's forward
-- migration DROPPED both (added 0067/0072, dropped 0079) and nothing re-adds them, so today
-- COUNT = 0 and their guard is a no-op. They are kept here only because 0079's Down/rollback path
-- re-adds both as VARCHAR(3), so a widen guard for them costs nothing and closes that edge case.
-- The three 3-char color-code columns (color.code, product.color_code, migration_0151_merge_conflict.
-- color_code) are deliberately NOT touched — they are not currency codes.
--
-- Non-destructive: widening only (VARCHAR(3)->VARCHAR(4), CHAR(3)->CHAR(4)); the type family, nullability,
-- default and comment of each column are restated verbatim so nothing else changes. Existing 3-char
-- values are preserved untouched. Indexes / UNIQUE / PRIMARY KEY on these columns (product_price,
-- shipment_carrier_price, complimentary_shipping_price, customer_order, costing_fx_rate, material_price)
-- are preserved by MODIFY COLUMN.
--
-- Crash-idempotent: MySQL 8 has no "widen if narrower". Each column is guarded on information_schema
-- (CHARACTER_MAXIMUM_LENGTH = 3): the ALTER runs only while the column is still 3 wide, so a mid-file
-- crash + next-boot re-run of the whole file skips every already-widened column, and a column that does
-- not exist (COUNT = 0) is skipped instead of erroring. ANSI_QUOTES is ON on the managed cluster, so
-- every string literal is single-quoted and the DEFAULT 'EUR' literal is doubled ('' -> ') inside the
-- dynamic SQL.

-- customer_order.currency  VARCHAR(3) NOT NULL DEFAULT 'EUR'
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'customer_order' AND COLUMN_NAME = 'currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE customer_order MODIFY COLUMN currency VARCHAR(4) NOT NULL DEFAULT ''EUR'' COMMENT ''ISO 4217 currency code (e.g., EUR, USD, JPY, CNY, KRW)''', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- product_price.currency  VARCHAR(3) NOT NULL  [UNIQUE (product_id, currency)]
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product_price' AND COLUMN_NAME = 'currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE product_price MODIFY COLUMN currency VARCHAR(4) NOT NULL COMMENT ''ISO 4217 currency code (e.g., EUR, USD, JPY, CNY, KRW)''', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- shipment_carrier_price.currency  VARCHAR(3) NOT NULL  [UNIQUE (shipment_carrier_id, currency)]
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'shipment_carrier_price' AND COLUMN_NAME = 'currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE shipment_carrier_price MODIFY COLUMN currency VARCHAR(4) NOT NULL COMMENT ''ISO 4217 currency code (e.g., EUR, USD, JPY, CNY, KRW, GBP)''', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- complimentary_shipping_price.currency  VARCHAR(3) NOT NULL  [UNIQUE (currency)]
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'complimentary_shipping_price' AND COLUMN_NAME = 'currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE complimentary_shipping_price MODIFY COLUMN currency VARCHAR(4) NOT NULL COMMENT ''ISO 4217 currency code (e.g., USD, EUR, JPY, CNY, KRW)''', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- product_stock_change_history.paid_currency  VARCHAR(3) NULL
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product_stock_change_history' AND COLUMN_NAME = 'paid_currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE product_stock_change_history MODIFY COLUMN paid_currency VARCHAR(4) NULL COMMENT ''ISO 4217 currency code of payment''', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- product_stock_change_history.payout_base_currency  VARCHAR(3) NULL DEFAULT 'EUR'
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product_stock_change_history' AND COLUMN_NAME = 'payout_base_currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE product_stock_change_history MODIFY COLUMN payout_base_currency VARCHAR(4) NULL DEFAULT ''EUR'' COMMENT ''Base currency for payout (always EUR)''', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- costing_fx_rate.currency  CHAR(3) NOT NULL  [PRIMARY KEY (currency, valid_from)]
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'costing_fx_rate' AND COLUMN_NAME = 'currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE costing_fx_rate MODIFY COLUMN currency CHAR(4) NOT NULL COMMENT ''ISO 4217, uppercase''', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- material_price.currency  CHAR(3) NOT NULL  [PRIMARY KEY (material_id, valid_from, currency)]
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_price' AND COLUMN_NAME = 'currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE material_price MODIFY COLUMN currency CHAR(4) NOT NULL COMMENT ''ISO 4217, uppercase (fold to base via costing_fx_rate)''', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- material_lot.currency  CHAR(3) NULL
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot' AND COLUMN_NAME = 'currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE material_lot MODIFY COLUMN currency CHAR(4) NULL', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- material_stock_movement.currency  CHAR(3) NULL
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND COLUMN_NAME = 'currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE material_stock_movement MODIFY COLUMN currency CHAR(4) NULL', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- opex_line.currency  CHAR(3) NOT NULL
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'opex_line' AND COLUMN_NAME = 'currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE opex_line MODIFY COLUMN currency CHAR(4) NOT NULL', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- opex_recurring.currency  CHAR(3) NOT NULL
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'opex_recurring' AND COLUMN_NAME = 'currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE opex_recurring MODIFY COLUMN currency CHAR(4) NOT NULL', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- employee.default_currency  CHAR(3) NULL
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'employee' AND COLUMN_NAME = 'default_currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE employee MODIFY COLUMN default_currency CHAR(4) NULL', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- production_run.planned_currency  CHAR(3) NULL
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run' AND COLUMN_NAME = 'planned_currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE production_run MODIFY COLUMN planned_currency CHAR(4) NULL COMMENT ''currency of planned_unit_cost''', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- production_run_cost.currency  CHAR(3) NOT NULL
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_cost' AND COLUMN_NAME = 'currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE production_run_cost MODIFY COLUMN currency CHAR(4) NOT NULL COMMENT ''ISO 4217, uppercase''', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- tech_card_bom_item.currency  VARCHAR(3) NULL
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE tech_card_bom_item MODIFY COLUMN currency VARCHAR(4) NULL COMMENT ''ISO 4217 for unit_price''', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- tech_card_costing.currency  VARCHAR(3) NULL
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_costing' AND COLUMN_NAME = 'currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE tech_card_costing MODIFY COLUMN currency VARCHAR(4) NULL COMMENT ''ISO 4217 for the costing articles''', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- tech_card_dev_expense.currency  CHAR(3) NOT NULL
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_dev_expense' AND COLUMN_NAME = 'currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE tech_card_dev_expense MODIFY COLUMN currency CHAR(4) NOT NULL COMMENT ''ISO 4217, uppercase''', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- tech_card_release.currency  CHAR(3) NULL
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_release' AND COLUMN_NAME = 'currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE tech_card_release MODIFY COLUMN currency CHAR(4) NULL COMMENT ''currency of unit_cost''', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- tech_card.currency  VARCHAR(3) NULL  (added 0067, DROPPED by 0079's forward migration; not a
-- live column today — see the file header note. Guarded defensively in case 0079 is ever rolled
-- back (its Down path re-adds this column as VARCHAR(3)); COUNT = 0 now, so this is a no-op.
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE tech_card MODIFY COLUMN currency VARCHAR(4) NULL COMMENT ''ISO 4217 for target cost/price and costing''', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- tech_card_construction.labour_rate_currency  VARCHAR(3) NULL  (added 0072, DROPPED by 0079's
-- forward migration; not live today, guarded for the same rollback-edge-case reason as
-- tech_card.currency above. COUNT = 0 now, so this is a no-op.
SET @need := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_construction' AND COLUMN_NAME = 'labour_rate_currency' AND CHARACTER_MAXIMUM_LENGTH = 3) = 1;
SET @sql := IF(@need, 'ALTER TABLE tech_card_construction MODIFY COLUMN labour_rate_currency VARCHAR(4) NULL COMMENT ''ISO 4217 for labour_rate''', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- +migrate Down
-- Intentionally a no-op. This migration only WIDENS columns (3 -> 4), which is non-destructive: a
-- VARCHAR(4)/CHAR(4) column holding only <=3-char codes behaves identically to the old (3) column.
-- Narrowing back to (3) would TRUNCATE any 4-char code already stored (a backfilled USDT price from
-- 0186, or a manually-booked USDT order) and hard-fail under STRICT mode, so there is nothing safe to
-- reverse. Prod is forward-only (automigrate up); a deliberate rollback removes USDT data via 0186's
-- Down first and can leave the columns harmlessly wide.
SELECT 1;

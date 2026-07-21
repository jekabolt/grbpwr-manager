-- +migrate Up
-- Accounting phase 2, wave 1 (VAT engine). Add the resolved VAT regime + buyer VAT id to an order,
-- and seed the three VAT/B2B chart-of-accounts rows the sale/purchase rules need.
--
-- vat_regime is the snapshot of the resolver's decision (internal/accounting/vatregime.go),
-- written by the acctposting worker in the SAME tx that posts the order-sale entry. buyer_vat_id is
-- the B2B customer's VAT identifier (custom orders only; storefront orders send NULL). Both are
-- additive, nullable, and crash-idempotent (guarded on information_schema — MySQL 8 has no
-- ADD COLUMN IF NOT EXISTS, so a mid-file failure re-runs from the top).

-- --- customer_order: vat_regime + buyer_vat_id (guarded on vat_regime; one atomic ALTER or none) ---
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'customer_order' AND COLUMN_NAME = 'vat_regime');
SET @sql := IF(@need,
    'ALTER TABLE customer_order
        ADD COLUMN vat_regime VARCHAR(24) NULL
            COMMENT ''VAT regime snapshotted at posting: oss|pl_domestic|export|wdt|uk_stock_domestic|none'',
        ADD COLUMN buyer_vat_id VARCHAR(32) NULL
            COMMENT ''B2B buyer VAT id (custom orders only); drives wdt/reverse-charge and 4310 revenue'',
        ADD CONSTRAINT chk_customer_order_vat_regime
            CHECK (vat_regime IN (''oss'',''pl_domestic'',''export'',''wdt'',''uk_stock_domestic'',''none''))',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- --- seed VAT / B2B accounts (INSERT ... SELECT ... WHERE NOT EXISTS, the idempotent 0190 pattern) ---
-- 2080 VAT Input (Recoverable): liability contra (Dr on a domestic purchase / WNT self-charge).
-- 4310 Sales – B2B / Wholesale: revenue for orders carrying a buyer VAT id.
-- 4050 Trade Discounts (B2B): revenue contra, seeded now; posting arrives with B2B discounts.
INSERT INTO acct_account (code, name, section, statement, is_system)
SELECT * FROM (SELECT
    '2080' code, 'VAT Input (Recoverable)' name, 'liability' section, 'BS' statement, TRUE is_system UNION ALL SELECT
    '4310', 'Sales – B2B / Wholesale',              'revenue',   'PL', TRUE UNION ALL SELECT
    '4050', 'Trade Discounts (B2B)',                'revenue',   'PL', TRUE
) seed
WHERE NOT EXISTS (SELECT 1 FROM acct_account a WHERE a.code = seed.code);

-- +migrate Down
SET @has := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'customer_order' AND COLUMN_NAME = 'vat_regime');
SET @sql := IF(@has,
    'ALTER TABLE customer_order
        DROP CONSTRAINT chk_customer_order_vat_regime,
        DROP COLUMN buyer_vat_id,
        DROP COLUMN vat_regime',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
-- seed rows are intentionally not removed (down on seed data is unsafe once referenced).

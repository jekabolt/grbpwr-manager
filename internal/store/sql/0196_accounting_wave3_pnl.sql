-- +migrate Up
-- Accounting phase 2, wave 3 (full P&L). Seed the four accounts the P&L-completion features need,
-- and extend two enum CHECKs additively: the account-section CHECK gains 'tax' (Corporation Tax lives
-- in its own P&L section) and the journal-entry source_type CHECK gains the two new pull sources
-- ('shipping_actual' for 6030 actual carrier cost, 'dev_expense' for 6210 R&D). Additive only — every
-- existing row already satisfies the widened CHECKs. MySQL 8 DDL is not transactional, so each step is
-- crash-idempotent (information_schema-guarded PREPARE, the canonical repo pattern 07 §7.2 /
-- 0157_material_cti.sql / 0195); the named CHECKs are dropped by their stable name, never by an
-- auto-generated <table>_chk_<n>.

-- --- extend chk_acct_account_section (+tax) — MUST run BEFORE the seed ---
-- 8010 Corporation Tax is seeded with the NEW 'tax' section, so the CHECK has to allow it first. MySQL 8
-- enforces CHECKs, and a multi-row INSERT…SELECT where one row violates the CHECK fails the WHOLE
-- statement (ER_CHECK_CONSTRAINT_VIOLATED) → migration 0196 aborts → the app (MYSQL_AUTOMIGRATE) will not
-- start and cannot self-heal. So the section-CHECK widening precedes the seed.
SET @sql := IF((SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'acct_account'
      AND CONSTRAINT_NAME = 'chk_acct_account_section') > 0,
    'ALTER TABLE acct_account DROP CONSTRAINT chk_acct_account_section', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

SET @sql := IF((SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'acct_account'
      AND CONSTRAINT_NAME = 'chk_acct_account_section') = 0,
    'ALTER TABLE acct_account ADD CONSTRAINT chk_acct_account_section CHECK (section IN (
        ''asset'',''liability'',''equity'',''revenue'',''cogs'',''opex'',''tax''))', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- --- seed 6030 / 4030 / 2050 / 8010 (0195 INSERT…SELECT…WHERE NOT EXISTS idempotent seed) ---
-- 6030: OPEX — actual carrier shipping & fulfilment cost, posted from shipment.actual_cost /
--       return_shipping_cost by the shipping_actual pull (3.1). Pairs the 4110 shipping income that
--       previously had no expense side.
-- 4030: REVENUE contra (debit-normal) — Discounts / Promotions. The order-sale / delivered-sale
--       builders split a promo order's gross into the full-price revenue credit + this discount debit
--       (3.3). P&L total is unchanged; the line is analytics only.
-- 2050: LIABILITY — Income Tax Payable, credited by the accountant's manual Corporation-Tax journal.
-- 8010: TAX (its own P&L section) — Corporation Tax, debited by that same manual journal. There is NO
--       auto-accrual (UK CT is computed by the accountant); the account exists so the P&L can show a
--       Net-Profit-after-tax line once the manual entry is posted (3.4).
INSERT INTO acct_account (code, name, section, statement, is_system)
SELECT * FROM (SELECT
    '6030' code, 'Shipping & Fulfillment'  name, 'opex'      section, 'PL' statement, TRUE is_system UNION ALL SELECT
    '4030', 'Discounts / Promotions',            'revenue',   'PL', TRUE  UNION ALL SELECT
    '2050', 'Income Tax Payable',                'liability', 'BS', TRUE  UNION ALL SELECT
    '8010', 'Corporation Tax',                   'tax',       'PL', TRUE
) seed
WHERE NOT EXISTS (SELECT 1 FROM acct_account a WHERE a.code = seed.code);

-- --- extend chk_acct_entry_source_type (+shipping_actual, +dev_expense) ---
SET @sql := IF((SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'acct_journal_entry'
      AND CONSTRAINT_NAME = 'chk_acct_entry_source_type') > 0,
    'ALTER TABLE acct_journal_entry DROP CONSTRAINT chk_acct_entry_source_type', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

SET @sql := IF((SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'acct_journal_entry'
      AND CONSTRAINT_NAME = 'chk_acct_entry_source_type') = 0,
    'ALTER TABLE acct_journal_entry ADD CONSTRAINT chk_acct_entry_source_type CHECK (source_type IN (
        ''order_sale'',''order_refund'',
        ''order_prepayment'',''order_transit'',''order_delivered_sale'',
        ''material_receipt'',''material_issue'',''material_return'',
        ''material_writeoff'',''material_adjustment'',
        ''production_receive'',''opex_month'',
        ''shipping_actual'',''dev_expense'',
        ''manual'',''reversal''))', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- +migrate Down
-- Deliberately irreversible (no-op). Narrowing either CHECK after wave-3 rows exist would reject
-- already-recorded shipping_actual / dev_expense entries and any 'tax'-section account; the seeded
-- 6030/4030/2050/8010 accounts may already be referenced by append-only journal lines. Corrections to
-- ledger data are reversal entries, never destructive migration edits (same stance as 0190/0195 Downs).
SELECT 1;

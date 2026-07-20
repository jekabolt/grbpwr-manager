-- +migrate Up
-- Accounting phase 2, wave 2 (delivered revenue recognition). Seed the two balance-sheet accounts the
-- prepayment/transit chain needs, and extend the journal-entry source_type + outbox event_type enum
-- CHECKs with the wave-2 values. Additive only — every existing row already satisfies the widened
-- CHECKs. MySQL 8 DDL is not transactional, so each step is crash-idempotent (information_schema-guarded
-- PREPARE, the canonical repo pattern 07 §7.2 / 0157_material_cti.sql); the named CHECKs are dropped by
-- their stable name and never by an auto-generated <table>_chk_<n>.

-- --- seed 2090 Customer Prepayments (liability) + 1140 Inventory in Transit (asset) ---
-- 2090: customer money received but revenue not yet recognised under the delivered model — credited at
--       payment (order_prepayment), drained at delivered (order_delivered_sale), unwound on a
--       pre-delivered refund.
-- 1140: OUTBOUND transit ONLY — finished goods shipped to the buyer and not yet delivered: Dr at shipped
--       from 1130, Cr into 5010 COGS at delivered. Inbound purchase transit is NOT modelled (materials
--       are capitalised at receive) — the name is Excel-compatible; the scope is fixed by this comment.
INSERT INTO acct_account (code, name, section, statement, is_system)
SELECT * FROM (SELECT
    '2090' code, 'Customer Prepayments'   name, 'liability' section, 'BS' statement, TRUE is_system UNION ALL SELECT
    '1140', 'Inventory in Transit',             'asset',     'BS', TRUE
) seed
WHERE NOT EXISTS (SELECT 1 FROM acct_account a WHERE a.code = seed.code);

-- --- extend chk_acct_entry_source_type (+order_prepayment, +order_transit, +order_delivered_sale) ---
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
        ''production_receive'',''opex_month'',''manual'',''reversal''))', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- --- extend chk_acct_event_type (+order_shipped, +order_delivered) ---
SET @sql := IF((SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'acct_event'
      AND CONSTRAINT_NAME = 'chk_acct_event_type') > 0,
    'ALTER TABLE acct_event DROP CONSTRAINT chk_acct_event_type', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

SET @sql := IF((SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'acct_event'
      AND CONSTRAINT_NAME = 'chk_acct_event_type') = 0,
    'ALTER TABLE acct_event ADD CONSTRAINT chk_acct_event_type CHECK (event_type IN (
        ''order_paid'',''order_refund'',''order_shipped'',''order_delivered''))', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- +migrate Down
-- Deliberately irreversible (no-op). Narrowing either CHECK after wave-2 rows exist would reject
-- already-recorded order_prepayment/order_transit/order_delivered_sale entries and order_shipped/
-- order_delivered events; the seeded 2090/1140 accounts may already be referenced by append-only
-- journal lines. Corrections to ledger data are reversal entries, never destructive migration edits
-- (same stance as 0190/0191 seed Downs).
SELECT 1;

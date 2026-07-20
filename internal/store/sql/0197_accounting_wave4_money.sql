-- +migrate Up
-- Accounting phase 2, wave 4 (money side): the Revolut bank inbox (4.1), Stripe disputes (4.3) and the
-- AP/AR subledgers (4.4). Additive only — every existing row already satisfies the two widened CHECKs,
-- and every new object is created guarded so a mid-file crash re-runs the whole file idempotently (MySQL
-- 8 DDL is not transactional). The named CHECKs are dropped by their stable name, never an auto-generated
-- <table>_chk_<n> (07 §7.2 / 0157 / 0195 / 0196).

-- =====================================================================================
-- 4.3 Stripe disputes: extend the journal-entry source_type CHECK (+order_dispute) and the outbox
-- event_type CHECK (+order_dispute). A chargeback books a manual-provenance Dr 4040 / Dr 6050 / Cr 1030
-- entry (order_dispute) enqueued as an order_dispute outbox event from the Stripe webhook.
-- =====================================================================================

-- --- extend chk_acct_entry_source_type (+order_dispute) ---
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
        ''order_dispute'',
        ''manual'',''reversal''))', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- --- extend chk_acct_event_type (+order_dispute) ---
SET @sql := IF((SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'acct_event'
      AND CONSTRAINT_NAME = 'chk_acct_event_type') > 0,
    'ALTER TABLE acct_event DROP CONSTRAINT chk_acct_event_type', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

SET @sql := IF((SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'acct_event'
      AND CONSTRAINT_NAME = 'chk_acct_event_type') = 0,
    'ALTER TABLE acct_event ADD CONSTRAINT chk_acct_event_type CHECK (event_type IN (
        ''order_paid'',''order_refund'',''order_shipped'',''order_delivered'',''order_dispute''))', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- =====================================================================================
-- 4.1 Revolut CSV inbox. acct_bank_txn is the parsed, deduplicated inbox of statement lines (external_id
-- is the composite Revolut id + ':' + payment currency, so the two legs of an EXCHANGE — same id, different
-- currency — both survive UNIQUE); acct_bank_rule holds the substring→account suggestions applied at import.
-- amount is SIGNED (negative = outflow → drives Dr/Cr in PostBankTxn); a non-EUR line posts via the phase-1
-- amount_src / currency_src FX mechanic. matched_entry_id links a posted line to its journal entry.
-- =====================================================================================
CREATE TABLE IF NOT EXISTS acct_bank_txn (
    id                INT AUTO_INCREMENT PRIMARY KEY,
    source            VARCHAR(16)  NOT NULL DEFAULT 'revolut',
    external_id       VARCHAR(128) NOT NULL,                 -- Revolut ID + ':' + Payment currency (dedup)
    booked_at         DATETIME     NOT NULL,                 -- Date completed (UTC), fallback Date started
    amount            DECIMAL(14,2) NOT NULL,                -- signed: negative = outflow
    currency          VARCHAR(4)   NOT NULL,                 -- Payment currency (PLN/GBP/EUR/USDT...)
    fee               DECIMAL(14,2) NULL,                    -- Fee column (informational; not auto-posted)
    description        VARCHAR(512) NOT NULL DEFAULT '',
    counterparty      VARCHAR(255) NULL,
    state             VARCHAR(16)  NOT NULL DEFAULT 'unmatched',
    matched_entry_id  INT          NULL,                     -- journal entry created by PostBankTxn
    suggested_account VARCHAR(8)   NULL,                     -- rule/default hint prefilled in the post modal
    raw               JSON         NOT NULL,                 -- the whole CSV row (header-keyed)
    created_at        TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_acct_bank_txn_external (external_id),
    KEY idx_acct_bank_txn_state (state, booked_at),
    CONSTRAINT chk_acct_bank_txn_state CHECK (state IN ('unmatched','matched','posted','ignored')),
    CONSTRAINT fk_acct_bank_txn_entry FOREIGN KEY (matched_entry_id)
        REFERENCES acct_journal_entry(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS acct_bank_rule (
    id           INT AUTO_INCREMENT PRIMARY KEY,
    pattern      VARCHAR(255) NOT NULL,                      -- substring matched against counterparty/description
    account_code VARCHAR(8)   NOT NULL,                      -- suggested account for a matching line
    created_at   TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- =====================================================================================
-- 4.4 AP/AR subledgers. supplier is the purchase-side catalog; material_stock_movement.supplier_id tags a
-- receipt with its supplier, and acct_journal_entry.supplier_id carries that tag onto the posted M1 entry
-- (Cr 2010) so GetPayables can group open Accounts-Payable by supplier. AR needs no new column — bank-invoice
-- orders already debit 1040, so GetReceivables groups 1040 by order from the ledger.
-- =====================================================================================
CREATE TABLE IF NOT EXISTS supplier (
    id         INT AUTO_INCREMENT PRIMARY KEY,
    name       VARCHAR(255) NOT NULL,
    vat_id     VARCHAR(64)  NULL,
    notes      VARCHAR(512) NULL,
    created_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_supplier_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- --- material_stock_movement.supplier_id (nullable FK, guarded — mirrors 0117 product_id) ---
SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND COLUMN_NAME = 'supplier_id');
SET @sql := IF(@need_col,
    'ALTER TABLE material_stock_movement ADD COLUMN supplier_id INT NULL', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement'
      AND CONSTRAINT_NAME = 'fk_msm_supplier');
SET @sql := IF(@need_fk,
    'ALTER TABLE material_stock_movement
        ADD CONSTRAINT fk_msm_supplier FOREIGN KEY (supplier_id) REFERENCES supplier(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

SET @need_idx := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND INDEX_NAME = 'idx_msm_supplier');
SET @sql := IF(@need_idx,
    'ALTER TABLE material_stock_movement ADD INDEX idx_msm_supplier (supplier_id)', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- --- acct_journal_entry.supplier_id (nullable FK, guarded — carries the AP supplier tag) ---
SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'acct_journal_entry' AND COLUMN_NAME = 'supplier_id');
SET @sql := IF(@need_col,
    'ALTER TABLE acct_journal_entry ADD COLUMN supplier_id INT NULL', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'acct_journal_entry'
      AND CONSTRAINT_NAME = 'fk_acct_entry_supplier');
SET @sql := IF(@need_fk,
    'ALTER TABLE acct_journal_entry
        ADD CONSTRAINT fk_acct_entry_supplier FOREIGN KEY (supplier_id) REFERENCES supplier(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

SET @need_idx := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'acct_journal_entry' AND INDEX_NAME = 'idx_acct_entry_supplier');
SET @sql := IF(@need_idx,
    'ALTER TABLE acct_journal_entry ADD INDEX idx_acct_entry_supplier (supplier_id)', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- +migrate Down
-- Deliberately irreversible (no-op). Narrowing either CHECK after wave-4 rows exist would reject
-- already-recorded order_dispute entries/events; the new acct_bank_txn / acct_bank_rule / supplier tables
-- and the supplier_id columns may already be referenced by append-only ledger/inbox rows. Corrections to
-- ledger data are reversal entries, never destructive migration edits (same stance as 0195/0196 Downs).
SELECT 1;

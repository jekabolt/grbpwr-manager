-- +migrate Up
-- Persist the operator's ignore reason on bank inbox lines (audit finding F-7 in
-- docs/plan-accounting-phase2/12-audit-findings.md): IgnoreBankTxnRequest.reason was accepted by
-- the API and silently discarded. An ignored line books NOTHING, so the reason is its only trace —
-- without it a real expense wrongly marked ignored is unreviewable.
--
-- Idempotent: MySQL 8 has no ADD COLUMN IF NOT EXISTS, so the ALTER is guarded via
-- information_schema (same PREPARE/EXECUTE pattern as 0195/0201) and a mid-file crash re-runs the
-- whole file safely.
SET @sql := IF((SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'acct_bank_txn'
      AND COLUMN_NAME = 'ignore_reason') = 0,
    'ALTER TABLE acct_bank_txn ADD COLUMN ignore_reason VARCHAR(255) NULL DEFAULT NULL AFTER suggested_account',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
-- No-op: dropping the column would erase operator-entered audit context; the column is nullable
-- and additive, so rolling back the code without the migration is safe.

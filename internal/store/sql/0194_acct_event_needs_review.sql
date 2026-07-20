-- +migrate Up
-- Accounting phase 2 review follow-ups (docs/plan-accounting-phase2/09-code-review-findings.md,
-- H-1/H-2/B-5). `needs_review` flags an event that was terminally disposed because it could NOT be
-- posted automatically and needs an operator: a non-EUR/degenerate order that needs a manual entry
-- (H-1), an orphan refund whose sale will never post (H-2), or a dead-lettered event that failed too
-- many times (B-5). It is set together with processed_at (so the event leaves the pending window and
-- stops retrying), but stays visible so ClosePeriod blocks the month until it is resolved. Cleared by
-- ReprocessAcctEvent (reset for retry) or ResolveAcctEvent (manually handled).
--
-- Additive, nullable-free (default 0), crash-idempotent (guarded on information_schema — MySQL 8 has no
-- ADD COLUMN IF NOT EXISTS, so a mid-file failure re-runs from the top).
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'acct_event' AND COLUMN_NAME = 'needs_review');
SET @sql := IF(@need,
    'ALTER TABLE acct_event
        ADD COLUMN needs_review TINYINT(1) NOT NULL DEFAULT 0
            COMMENT ''terminally disposed but needs an operator (manual entry / orphan refund / dead-letter)'',
        ADD KEY idx_acct_event_review (needs_review, id)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
SET @has := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'acct_event' AND COLUMN_NAME = 'needs_review');
SET @sql := IF(@has,
    'ALTER TABLE acct_event DROP KEY idx_acct_event_review, DROP COLUMN needs_review',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Up

-- production-costing Phase 5 (plan 05): partial receipts.
--   final              — the receipt that declared the run complete (flipped it to 'received').
--                        Every pre-Phase-5 receipt WAS the run's one final receipt, so existing
--                        rows backfill to 1.
--   posted_manual_base — the manual-cost amount this receipt's live journal entry capitalised
--                        (Dr 1120 / Cr 2010), written by the posting worker in the SAME tx as the
--                        entry insert. Later receipts of the run read the SUM over their siblings
--                        to capitalise only the still-uncapitalised delta ("capitalize once").
--   posted_fg_base     — the WIP→FG amount this receipt's live entry transferred (Dr 1130 /
--                        Cr 1120); the pro-rata/true-up arithmetic reads the sibling SUM so the
--                        run's good-unit share is transferred exactly once regardless of posting
--                        order, and rounding never strands money on 1120.

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_receipt' AND COLUMN_NAME = 'final');
SET @sql := IF(@have_col = 0,
    'ALTER TABLE production_run_receipt ADD COLUMN final TINYINT(1) NOT NULL DEFAULT 0 COMMENT ''this receipt declared the run complete (Phase 5)''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Every pre-Phase-5 receipt was final by construction (receipt v1 was final-only). Guarded by the
-- column's own freshness: only rows that still carry the ADD COLUMN default AND belong to a run
-- the old flow actually closed. Re-runs are no-ops (the WHERE matches nothing new).
UPDATE production_run_receipt pr
JOIN production_run r ON r.id = pr.run_id
SET pr.final = 1
WHERE pr.final = 0 AND r.status IN ('received', 'closed');

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_receipt' AND COLUMN_NAME = 'posted_manual_base');
SET @sql := IF(@have_col = 0,
    'ALTER TABLE production_run_receipt ADD COLUMN posted_manual_base DECIMAL(12, 2) NULL COMMENT ''manual cost capitalised by this receipt''''s live entry (posting bookkeeping)''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_receipt' AND COLUMN_NAME = 'posted_fg_base');
SET @sql := IF(@have_col = 0,
    'ALTER TABLE production_run_receipt ADD COLUMN posted_fg_base DECIMAL(12, 2) NULL COMMENT ''WIP to FG amount transferred by this receipt''''s live entry (posting bookkeeping)''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Posted receipts predating these columns: their live entries' amounts are recoverable from the
-- journal (Cr 2010 = manual, Dr 1130 = fg per entry). The worker's raced-path recovery does exactly
-- that at runtime, and Phase 5 arithmetic only ever reads the SUM over siblings — a NULL here reads
-- as 0, which UNDERSTATES prior postings and would make a later receipt over-relieve. So backfill
-- posted finals now, from their live entries, both key families, colons via CHAR(58).
UPDATE production_run_receipt pr
JOIN acct_journal_entry e
    ON e.source_type = 'production_receive' AND e.reversed_by IS NULL
   AND (e.source_key = CONCAT('receipt', CHAR(58), CAST(pr.id AS CHAR CHARACTER SET utf8mb4)) COLLATE utf8mb4_unicode_ci
        OR e.source_key LIKE CONCAT('receipt', CHAR(58), CAST(pr.id AS CHAR CHARACTER SET utf8mb4), CHAR(58), 'v%') COLLATE utf8mb4_unicode_ci
        OR e.source_key = CAST(pr.run_id AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci
        OR e.source_key LIKE CONCAT(CAST(pr.run_id AS CHAR CHARACTER SET utf8mb4), CHAR(58), 'v%') COLLATE utf8mb4_unicode_ci)
SET pr.posted_manual_base = COALESCE((SELECT SUM(l.amount) FROM acct_journal_line l
                                      JOIN acct_account a ON a.id = l.account_id
                                      WHERE l.entry_id = e.id AND a.code = '2010' AND l.side = 'credit'), 0),
    pr.posted_fg_base = COALESCE((SELECT SUM(l.amount) FROM acct_journal_line l
                                  JOIN acct_account a ON a.id = l.account_id
                                  WHERE l.entry_id = e.id AND a.code = '1130' AND l.side = 'debit'), 0)
WHERE pr.posting_status = 'posted' AND pr.posted_manual_base IS NULL AND pr.posted_fg_base IS NULL;

-- +migrate Down

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_receipt' AND COLUMN_NAME = 'posted_fg_base');
SET @sql := IF(@have_col > 0, 'ALTER TABLE production_run_receipt DROP COLUMN posted_fg_base', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_receipt' AND COLUMN_NAME = 'posted_manual_base');
SET @sql := IF(@have_col > 0, 'ALTER TABLE production_run_receipt DROP COLUMN posted_manual_base', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_receipt' AND COLUMN_NAME = 'final');
SET @sql := IF(@have_col > 0, 'ALTER TABLE production_run_receipt DROP COLUMN final', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

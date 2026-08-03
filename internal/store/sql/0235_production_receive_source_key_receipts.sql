-- +migrate Up

-- production-costing Phase 4 (plan file 05, amendment 9): the accounting unit of a production
-- receive becomes the RECEIPT, so production_receive source keys move from the run-id family
-- ('<run_id>' / '<run_id>:vN') to the receipt family ('receipt:<receipt_id>' / ':vN'). 0231 just
-- guaranteed every received run has exactly one backfilled receipt, so the mapping is 1:1 by
-- run_id; the version suffix is carried over verbatim. On prod this is a no-op twice over: the
-- ledger is empty (ACCOUNTING_ENABLED unset) and the WHERE matches nothing.
--
-- The worker and every predicate still match the legacy family as belt-and-suspenders, so an entry
-- that somehow escapes this rewrite (or a mid-deploy read) resolves identically.
UPDATE acct_journal_entry e
JOIN production_run_receipt pr
    ON pr.run_id = CAST(SUBSTRING_INDEX(e.source_key, ':', 1) AS UNSIGNED)
SET e.source_key = CONCAT('receipt:', pr.id,
    IF(LOCATE(':v', e.source_key) > 0, SUBSTRING(e.source_key, LOCATE(':v', e.source_key)), ''))
WHERE e.source_type = 'production_receive'
  AND e.source_key NOT LIKE 'receipt:%'
  AND e.source_key REGEXP '^[0-9]+(:v[0-9]+)?$';

-- A receipt whose entry ALREADY exists (0231 backfilled the receipt, the rewrite above just gave
-- the entry its receipt key) is already posted — mark it so. Without this, such receipts sit at
-- 'pending' forever: the scan's NOT-EXISTS skips them (the entry exists), so the worker never
-- touches them, and the backlog gauge WARN-alerts about a queue no one can drain on every tick.
-- The legacy run-id family is matched beside the receipt family for an entry the rewrite could not
-- reach (no receipt for its run) — same belt-and-suspenders as the worker's predicates.
UPDATE production_run_receipt pr
SET pr.posting_status = 'posted'
WHERE pr.posting_status = 'pending'
  AND EXISTS (SELECT 1 FROM acct_journal_entry e
              WHERE e.source_type = 'production_receive'
                AND (e.source_key = CONCAT('receipt:', CAST(pr.id AS CHAR CHARACTER SET utf8mb4)) COLLATE utf8mb4_unicode_ci
                     OR e.source_key LIKE CONCAT('receipt:', CAST(pr.id AS CHAR CHARACTER SET utf8mb4), ':v%') COLLATE utf8mb4_unicode_ci
                     OR e.source_key = CAST(pr.run_id AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci
                     OR e.source_key LIKE CONCAT(CAST(pr.run_id AS CHAR CHARACTER SET utf8mb4), ':v%') COLLATE utf8mb4_unicode_ci)
                AND e.reversed_by IS NULL);

-- +migrate Down

-- Reverse mapping: strip the receipt prefix back to the run id, keeping the version suffix.
UPDATE acct_journal_entry e
JOIN production_run_receipt pr
    ON CONCAT('receipt:', pr.id) = SUBSTRING_INDEX(e.source_key, ':v', 1)
SET e.source_key = CONCAT(pr.run_id,
    IF(LOCATE(':v', e.source_key) > 0, SUBSTRING(e.source_key, LOCATE(':v', e.source_key)), ''))
WHERE e.source_type = 'production_receive'
  AND e.source_key LIKE 'receipt:%';

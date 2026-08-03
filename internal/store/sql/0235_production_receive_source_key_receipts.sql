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

-- +migrate Down

-- Reverse mapping: strip the receipt prefix back to the run id, keeping the version suffix.
UPDATE acct_journal_entry e
JOIN production_run_receipt pr
    ON CONCAT('receipt:', pr.id) = SUBSTRING_INDEX(e.source_key, ':v', 1)
SET e.source_key = CONCAT(pr.run_id,
    IF(LOCATE(':v', e.source_key) > 0, SUBSTRING(e.source_key, LOCATE(':v', e.source_key)), ''))
WHERE e.source_type = 'production_receive'
  AND e.source_key LIKE 'receipt:%';

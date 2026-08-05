-- +migrate Up
-- Widen tech_card.aux_subtype with `tote_bag` (шоппер) — the carrier the customer takes the purchase
-- away in and keeps using. It is its own sub-type rather than a flavour of `dust_bag` — a tote is cut,
-- sewn and costed as its own item (own BOM, pieces, operations), and the assembly bill has to be able
-- to say which carrier a style ships with.
--
-- Only the value-set CHECK moves. The purpose gate (chk_tech_card_aux_subtype_purpose, 0173) is
-- untouched, there are no column changes (VARCHAR(16) already fits) and no backfill, since `tote_bag`
-- is a value operators pick going forward. Existing dust_bag rows are NOT re-read as шоппер — the 0173
-- name heuristic mapped "shopper" to dust_bag, and silently re-labelling shipped classifications would
-- rewrite what an assembly bill already means. Re-file those by hand if any turn out to be totes.
--
-- Idempotent — the constraint is dropped BY ITS STABLE NAME and re-added only when the current
-- definition lacks the new value, so a mid-file failure re-runs cleanly and a second run is a no-op.

-- Drop only a constraint that is actually there AND still carries the old value set. Checked
-- separately from the add below so an interrupted run (constraint already dropped, not yet re-added)
-- does not try to drop what is gone.
SET @has_stale := (SELECT COUNT(*) FROM information_schema.CHECK_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND CONSTRAINT_NAME = 'chk_tech_card_aux_subtype'
      AND CHECK_CLAUSE NOT LIKE '%tote_bag%');

SET @sql := IF(@has_stale > 0,
    'ALTER TABLE tech_card DROP CHECK chk_tech_card_aux_subtype',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Re-read AFTER the drop. Absent (fresh or mid-run) → add; already widened → no-op.
SET @has_new := (SELECT COUNT(*) FROM information_schema.CHECK_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND CONSTRAINT_NAME = 'chk_tech_card_aux_subtype'
      AND CHECK_CLAUSE LIKE '%tote_bag%');

SET @sql := IF(@has_new = 0,
    'ALTER TABLE tech_card ADD CONSTRAINT chk_tech_card_aux_subtype CHECK (aux_subtype IS NULL OR aux_subtype REGEXP ''^(brand_label|care_label|size_label|hangtag|sticker|dust_bag|garment_case|tote_bag|box|insert|hanger|other)$'')',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
-- (leaves the widened CHECK; a Down is not exercised in prod automigrate)
SELECT 1;

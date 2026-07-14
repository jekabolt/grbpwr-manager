-- +migrate Up

-- Cleanup half of the packaging weight-unit change (see 0128): drop the old DECIMAL(8,3) kg columns
-- weight_net/weight_gross now that weight_net_grams/weight_gross_grams hold the values. Their inline
-- CHECK constraints from 0070 are auto-named, so they are looked up by their real name from
-- information_schema (filtered by clause) and dropped by that name — never a positional guess — before
-- the columns. Kept separate from 0128 so the additive backfill lands even if this drop needs a fix.
--
-- Idempotent: every step guarded via information_schema; a re-run after a partial apply is a no-op.

-- 1) Drop the old CHECK on weight_gross (the kg one, not the new *_grams check), if present.
SET @cn := (SELECT cc.CONSTRAINT_NAME
    FROM information_schema.CHECK_CONSTRAINTS cc
    JOIN information_schema.TABLE_CONSTRAINTS tc
      ON tc.CONSTRAINT_SCHEMA = cc.CONSTRAINT_SCHEMA AND tc.CONSTRAINT_NAME = cc.CONSTRAINT_NAME
    WHERE cc.CONSTRAINT_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_packaging'
      AND cc.CHECK_CLAUSE LIKE '%weight_gross%' AND cc.CHECK_CLAUSE NOT LIKE '%weight_gross_grams%'
    LIMIT 1);
SET @sql := IF(@cn IS NOT NULL, CONCAT('ALTER TABLE tech_card_packaging DROP CHECK `', @cn, '`'), 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 2) Drop the old CHECK on weight_net (the kg one, not the new *_grams check), if present.
SET @cn := (SELECT cc.CONSTRAINT_NAME
    FROM information_schema.CHECK_CONSTRAINTS cc
    JOIN information_schema.TABLE_CONSTRAINTS tc
      ON tc.CONSTRAINT_SCHEMA = cc.CONSTRAINT_SCHEMA AND tc.CONSTRAINT_NAME = cc.CONSTRAINT_NAME
    WHERE cc.CONSTRAINT_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_packaging'
      AND cc.CHECK_CLAUSE LIKE '%weight_net%' AND cc.CHECK_CLAUSE NOT LIKE '%weight_net_grams%'
    LIMIT 1);
SET @sql := IF(@cn IS NOT NULL, CONCAT('ALTER TABLE tech_card_packaging DROP CHECK `', @cn, '`'), 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 3) Drop the old kg columns, if present.
SET @has_old := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_packaging' AND COLUMN_NAME = 'weight_gross');
SET @sql := IF(@has_old,
    'ALTER TABLE tech_card_packaging DROP COLUMN weight_net, DROP COLUMN weight_gross',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Up

-- production-costing Phase 2 dead-schema drop (plan 01 §1.5): markup_multiplier / wholesale_price /
-- retail_price shipped with 0070 and were never wired — pricing lives on the published product, and
-- the entity struct has documented their absence ("Pricing was removed") for as long as it existed.
-- Zero Go references, verified by grep.
SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_costing' AND COLUMN_NAME = 'markup_multiplier');
SET @sql := IF(@have_col > 0, 'ALTER TABLE tech_card_costing DROP COLUMN markup_multiplier', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_costing' AND COLUMN_NAME = 'wholesale_price');
SET @sql := IF(@have_col > 0, 'ALTER TABLE tech_card_costing DROP COLUMN wholesale_price', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_costing' AND COLUMN_NAME = 'retail_price');
SET @sql := IF(@have_col > 0, 'ALTER TABLE tech_card_costing DROP COLUMN retail_price', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

SELECT 1;

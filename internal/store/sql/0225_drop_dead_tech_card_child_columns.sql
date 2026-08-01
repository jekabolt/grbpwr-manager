-- +migrate Up

-- Remove unused child-table columns after pattern metadata, the server-stamped revision journal,
-- and product ownership mirrors settled on their current read and write contracts. Every drop is
-- independently guarded so a retry after a partially applied migration is safe.

SET @drop_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_size_pattern' AND COLUMN_NAME = 'content_type');
SET @sql := IF(@drop_col, 'ALTER TABLE tech_card_size_pattern DROP COLUMN content_type', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @drop_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_revision' AND COLUMN_NAME = 'version');
SET @sql := IF(@drop_col, 'ALTER TABLE tech_card_revision DROP COLUMN version', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @drop_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_revision' AND COLUMN_NAME = 'revision_date');
SET @sql := IF(@drop_col, 'ALTER TABLE tech_card_revision DROP COLUMN revision_date', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @drop_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_revision' AND COLUMN_NAME = 'display_order');
SET @sql := IF(@drop_col, 'ALTER TABLE tech_card_revision DROP COLUMN display_order', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @drop_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_product' AND COLUMN_NAME = 'display_order');
SET @sql := IF(@drop_col, 'ALTER TABLE tech_card_product DROP COLUMN display_order', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- Destructive cleanup does not recreate retired data.
SELECT 1;

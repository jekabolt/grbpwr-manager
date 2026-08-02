-- +migrate Up

-- Remove retired tech card header fields after role assignments, release numbers, and the
-- server-stamped revision journal became authoritative. The release snapshot's legacy version was
-- copied from the retired header field and is removed with it. Every drop is independently guarded
-- so a retry after a partially applied migration is safe.

SET @drop_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'approved_by');
SET @sql := IF(@drop_col, 'ALTER TABLE tech_card DROP COLUMN approved_by', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @drop_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'designer');
SET @sql := IF(@drop_col, 'ALTER TABLE tech_card DROP COLUMN designer', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @drop_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'constructor');
SET @sql := IF(@drop_col, 'ALTER TABLE tech_card DROP COLUMN constructor', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @drop_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'technologist');
SET @sql := IF(@drop_col, 'ALTER TABLE tech_card DROP COLUMN technologist', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @drop_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'version');
SET @sql := IF(@drop_col, 'ALTER TABLE tech_card DROP COLUMN version', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @drop_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'revision_date');
SET @sql := IF(@drop_col, 'ALTER TABLE tech_card DROP COLUMN revision_date', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @drop_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_release' AND COLUMN_NAME = 'version');
SET @sql := IF(@drop_col, 'ALTER TABLE tech_card_release DROP COLUMN version', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- Destructive cleanup does not recreate retired data.
SELECT 1;

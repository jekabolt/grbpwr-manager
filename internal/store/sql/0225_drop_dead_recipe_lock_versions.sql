-- +migrate Up

-- style_assembly and packaging_recipe are full-replace sets. Their per-row lock versions were never
-- accepted, compared, incremented, or emitted, so every row remained zero and the columns could not
-- guard a set replacement. Each drop is independently guarded for safe retries.

SET @drop_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'style_assembly' AND COLUMN_NAME = 'lock_version');
SET @sql := IF(@drop_col, 'ALTER TABLE style_assembly DROP COLUMN lock_version', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @drop_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'packaging_recipe' AND COLUMN_NAME = 'lock_version');
SET @sql := IF(@drop_col, 'ALTER TABLE packaging_recipe DROP COLUMN lock_version', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- Destructive cleanup does not recreate retired schema.
SELECT 1;

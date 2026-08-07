-- +migrate Up

-- Ф5а.2. ONE visible, editable cutting coefficient per catalogue article, instead of eight named
-- losses nobody in the shop can measure separately. It covers, together: усадка, обход пороков,
-- сращивание, оттеночные полосы — the roll-level reality a marker cannot contain, because a marker
-- is measured on a clean lay of a nominal width.
--
-- Deliberately NOT here:
--   * no «класс ткани» taxonomy to pick a default from. That field does not exist in the system and
--     inventing one to feed defaults is exactly the disease of the first edition. ONE default, and
--     the value ranges (полотно ~3%, трикотаж ~6%, клетка/полоска +10–20%) live as UI hint text.
--   * no backfill. NULL means "nobody has set one", and the requirement path reads NULL as ×1.0, so
--     every existing plan keeps producing bit-for-bit the number it produced yesterday. Writing a
--     default of 1.03 here would silently inflate every material plan on prod overnight.
--
-- Stored as a MULTIPLIER (1.0300), not a percent: the requirement path multiplies by it, and a
-- percent would need a +1 nobody would remember to write. Range is guarded so a fat-fingered 103
-- (meaning "3%") cannot multiply a requirement by a hundred.
--
-- Idempotent: guarded ADD COLUMN + named CHECK.

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND COLUMN_NAME = 'cutting_coefficient');
SET @sql := IF(@need_col,
    'ALTER TABLE material ADD COLUMN cutting_coefficient DECIMAL(6,4) NULL AFTER fabric_weight_gsm',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material'
      AND CONSTRAINT_NAME = 'chk_material_cutting_coefficient');
SET @sql := IF(@need_chk,
    'ALTER TABLE material ADD CONSTRAINT chk_material_cutting_coefficient CHECK (cutting_coefficient IS NULL OR (cutting_coefficient >= 1 AND cutting_coefficient <= 3))',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

SET @has_chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material'
      AND CONSTRAINT_NAME = 'chk_material_cutting_coefficient');
SET @sql := IF(@has_chk, 'ALTER TABLE material DROP CONSTRAINT chk_material_cutting_coefficient', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND COLUMN_NAME = 'cutting_coefficient');
SET @sql := IF(@has_col, 'ALTER TABLE material DROP COLUMN cutting_coefficient', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Up

-- Ф5а.1. `material_lot` (0118) already IS the roll/партия entity — supplier lot code, received and
-- remaining quantity, movements attributed through material_stock_movement.lot_id, Receive/Issue/
-- List RPCs. It was missing exactly two facts the cutting floor measures when the roll arrives:
--
--   measured_width_cm — the width that ARRIVED, as opposed to the width the supplier printed. The
--     supplier says 150 and the roll measures 148, and the marker has to be made for the NARROWEST
--     width in the batch; material_fabric_attr.width_cm is the article's nominal, not this. NULL =
--     nobody measured it, which is not the same as "it matches the nominal".
--   shade_code — the dye lot / оттенок, for colour matching across rolls. NULL = unrecorded.
--
-- Measured LENGTH is deliberately NOT added: received_qty already is the measured length in the
-- material's unit, and a second column for it would be two numbers that must agree and eventually
-- would not.
--
-- Idempotent: guarded ADD COLUMN, so a re-run after a mid-file failure is a no-op.

SET @need_width := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot' AND COLUMN_NAME = 'measured_width_cm');
SET @sql := IF(@need_width,
    'ALTER TABLE material_lot ADD COLUMN measured_width_cm DECIMAL(6,2) NULL AFTER remaining_qty',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot'
      AND CONSTRAINT_NAME = 'chk_material_lot_measured_width');
SET @sql := IF(@need_chk,
    'ALTER TABLE material_lot ADD CONSTRAINT chk_material_lot_measured_width CHECK (measured_width_cm IS NULL OR measured_width_cm > 0)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_shade := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot' AND COLUMN_NAME = 'shade_code');
SET @sql := IF(@need_shade,
    'ALTER TABLE material_lot ADD COLUMN shade_code VARCHAR(64) NULL AFTER measured_width_cm',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

SET @has_chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot'
      AND CONSTRAINT_NAME = 'chk_material_lot_measured_width');
SET @sql := IF(@has_chk,
    'ALTER TABLE material_lot DROP CONSTRAINT chk_material_lot_measured_width',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_width := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot' AND COLUMN_NAME = 'measured_width_cm');
SET @sql := IF(@has_width, 'ALTER TABLE material_lot DROP COLUMN measured_width_cm', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_shade := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_lot' AND COLUMN_NAME = 'shade_code');
SET @sql := IF(@has_shade, 'ALTER TABLE material_lot DROP COLUMN shade_code', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

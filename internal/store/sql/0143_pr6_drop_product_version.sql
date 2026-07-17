-- +migrate Up

-- PR6 phase 4: drop the dead product.version column. It was the manual revision label that fed the
-- old generated SKU (0004); 0133 rebuilt the SKU as a plain minted value, leaving version referenced
-- by nothing. The read/write plumbing (query SELECTs, insert/update, entity, dto) has been removed, so
-- the column is now unused — dropping it requires the same commit (it is NOT NULL with no default, so
-- an insert can't omit it while it exists). No FK / CHECK / index / generated column references it.
--
-- Idempotent: guarded DROP COLUMN via information_schema (multi-line PREPARE/EXECUTE/DEALLOCATE — a
-- single line trips 1064 on the managed DSN, 0124).

SET @has_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'version');
SET @sql := IF(@has_col > 0, 'ALTER TABLE product DROP COLUMN version', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

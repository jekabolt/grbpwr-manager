-- +migrate Up
-- PLM-rework WS3 / S25: give the material catalog an optimistic-lock version so two concurrent
-- UpdateMaterial calls can't silently clobber each other. The store enforces the same double
-- guard as tech_card (in-Go compare + a load-bearing `WHERE lock_version = :expected`).
--
-- Additive + crash-idempotent: MySQL 8 has no ADD COLUMN IF NOT EXISTS, so the add is guarded on
-- information_schema (a mid-file DDL failure auto-commits and re-runs the file from the top, so
-- the guard must make a re-run a no-op).

SET @need_lock := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND COLUMN_NAME = 'lock_version');
SET @sql := IF(@need_lock,
    'ALTER TABLE material ADD COLUMN lock_version INT NOT NULL DEFAULT 0',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
ALTER TABLE material DROP COLUMN lock_version;

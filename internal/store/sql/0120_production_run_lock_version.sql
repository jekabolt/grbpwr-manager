-- +migrate Up

-- #9: optimistic-lock token on production_run. UpdateProductionRun is a full-replace of the run's
-- lines/costs/markers; without a version token a list→edit→save races a concurrent edit and the last
-- writer silently wins. lock_version is bumped on every UpdateProductionRun; a client that echoes the
-- value it read back as UpdateProductionRunRequest.expected_lock_version gets a concurrent edit
-- rejected (Aborted) instead of clobbered. Mirrors tech_card.lock_version.
--
-- DEFAULT 0 so existing rows and legacy clients (expected_lock_version = 0) keep the old
-- last-write-wins behaviour — the guard is opt-in, so this is backward-compatible.
--
-- Idempotent: guarded ADD COLUMN via information_schema (a mid-file re-run must not error on the
-- already-added column). The one-statement-per-line PREPARE/EXECUTE/DEALLOCATE form is required —
-- managed MySQL rejects them joined on one line without multiStatements.

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run' AND COLUMN_NAME = 'lock_version');
SET @sql := IF(@need_col,
    'ALTER TABLE production_run ADD COLUMN lock_version INT NOT NULL DEFAULT 0',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

SET @has_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run' AND COLUMN_NAME = 'lock_version');
SET @sql := IF(@has_col,
    'ALTER TABLE production_run DROP COLUMN lock_version',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
SELECT 1;

-- +migrate Up
-- PLM-rework WS6 / §2.7 + §5 M3 (destructive, behind a safety backfill). Drop the two deprecated
-- fitting columns the round-spine work replaced:
--   fitting.recorded_by             -> replaced by the server-stamped created_by (client-supplied
--                                      recorded_by let a caller write someone else's name, A3.3).
--   fitting_change_request.resolved -> replaced by the status enum (open|resolved), S26.
--
-- Safe against existing data (CLAUDE.md: a migration must never halt prod boot): each drop is preceded
-- by a re-runnable backfill that moves the value into its replacement column, so the drop provably
-- loses nothing — no STOP-guard that could halt boot is needed, the data is already preserved.
--
-- Crash-idempotent: MySQL 8 has no DROP COLUMN IF EXISTS, and a mid-file failure re-runs the whole file
-- from the top. So EVERY statement is guarded on information_schema COLUMN existence — including the
-- backfills, which reference recorded_by/resolved and would otherwise fail on the retry after the column
-- was already dropped. Lands after 0171 (which added created_by / status and did the first backfill).

-- fitting.recorded_by -> created_by/updated_by, then drop. All three steps skip once recorded_by is gone.
SET @has_rb := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting' AND COLUMN_NAME = 'recorded_by');
SET @sql := IF(@has_rb, 'UPDATE fitting SET created_by = recorded_by WHERE created_by = '''' AND recorded_by IS NOT NULL AND recorded_by <> ''''', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
SET @sql := IF(@has_rb, 'UPDATE fitting SET updated_by = recorded_by WHERE updated_by = '''' AND recorded_by IS NOT NULL AND recorded_by <> ''''', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
SET @sql := IF(@has_rb, 'ALTER TABLE fitting DROP COLUMN recorded_by', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- fitting_change_request.resolved -> status, then drop. Both steps skip once resolved is gone.
SET @has_res := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting_change_request' AND COLUMN_NAME = 'resolved');
SET @sql := IF(@has_res, 'UPDATE fitting_change_request SET status = ''resolved'' WHERE resolved = TRUE AND status <> ''resolved''', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
SET @sql := IF(@has_res, 'ALTER TABLE fitting_change_request DROP COLUMN resolved', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
-- Restore the dropped columns (best-effort; the backfilled values are not un-merged).
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting' AND COLUMN_NAME = 'recorded_by');
SET @sql := IF(@need, 'ALTER TABLE fitting ADD COLUMN recorded_by VARCHAR(255) NULL', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting_change_request' AND COLUMN_NAME = 'resolved');
SET @sql := IF(@need, 'ALTER TABLE fitting_change_request ADD COLUMN resolved BOOLEAN NOT NULL DEFAULT FALSE', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- BOARD-DONE: preserve the only recoverable closing date for legacy DONE cards before the
-- auto-archive worker changes task.updated_at.
--
-- `done` is the stored task.status value (TaskStatusDone). Existing status-history rows remain
-- exact. Only a currently-DONE task with no recorded transition into DONE receives an approximate
-- row, dated from its last task update and attributed to the system (changed_by = '').
--
-- The ADD is guarded because managed MySQL has no ADD COLUMN IF NOT EXISTS. The INSERT is also
-- idempotent: a retry after MySQL auto-committed either statement sees the new column and/or the
-- inserted DONE row and safely skips that part.

-- +migrate Up

SET @tsh_approximate := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'task_status_history'
      AND COLUMN_NAME = 'approximate');
SET @ddl := IF(@tsh_approximate = 0,
    'ALTER TABLE task_status_history
        ADD COLUMN approximate TINYINT(1) NOT NULL DEFAULT 0 COMMENT ''1 = closing time approximated from task.updated_at by migration 0419''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

INSERT INTO task_status_history
    (task_id, from_status, to_status, changed_by, changed_at, approximate)
SELECT t.id, NULL, 'done', '', t.updated_at, 1
FROM task t
WHERE t.status = 'done'
  AND NOT EXISTS (
      SELECT 1
      FROM task_status_history h
      WHERE h.task_id = t.id AND h.to_status = 'done'
  );

-- +migrate Down

SET @tsh_approximate_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'task_status_history'
      AND COLUMN_NAME = 'approximate');
SET @ddl_down := IF(@tsh_approximate_down = 1,
    'ALTER TABLE task_status_history DROP COLUMN approximate',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

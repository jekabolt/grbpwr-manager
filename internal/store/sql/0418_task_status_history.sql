-- +migrate Up
-- Status-only audit trail for the internal kanban. Board and position moves are deliberately
-- absent: this table answers when the work changed state, not how a card was arranged.
--
-- There is no backfill. GetTask synthesizes one status-less creation row from task.created_at /
-- task.created_by for pre-deploy cards, then returns every real transition recorded here.
-- CREATE TABLE IF NOT EXISTS keeps a retry safe after MySQL auto-committed the DDL but before
-- sql-migrate recorded the file.
CREATE TABLE IF NOT EXISTS task_status_history (
  id INT PRIMARY KEY AUTO_INCREMENT,
  task_id INT NOT NULL,
  from_status VARCHAR(16) NULL COMMENT 'NULL only for the initial status recorded at task creation',
  to_status VARCHAR(16) NOT NULL,
  changed_by VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'admin username from JWT; empty for system paths',
  changed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_task_status_history_task_changed (task_id, changed_at),
  CONSTRAINT fk_task_status_history_task
    FOREIGN KEY (task_id) REFERENCES task(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Task status transitions, newest first by changed_at/id';

-- +migrate Down
DROP TABLE IF EXISTS task_status_history;

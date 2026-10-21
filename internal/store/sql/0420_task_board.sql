-- +migrate Up

-- CUSTOM TASK BOARDS: boards become rows instead of a closed enum.
--
-- task_board holds every board. The six former enum boards are seeded with FIXED ids 1..6 (the
-- TaskBoard proto enum numbers) and keep their enum string as `slug`; a board created in the admin
-- has slug NULL. The slug is the bridge for the transition: an old client sends the enum, the
-- server maps it to the slug's row.
--
-- task.board_id is the placement from now on. task.board stays ONE more deploy so a rolled-back
-- binary keeps working; the new binary writes it as the slug for a seeded board and as
-- 'custom-<id>' for a custom one (unique per board, so an old binary's (board,status,position)
-- arithmetic stays per-board correct). Its CHECK is dropped below, which is what lets that value in.
-- The column drop is listed in README-pending-drops.md.
--
-- board_id's DEFAULT is an expression over `board`. It does two jobs:
--   * the ADD fills every existing row from its board in the same ALTER (no separate backfill pass);
--   * an OLD binary that INSERTs a task without naming board_id (deploy overlap or rollback) still
--     gets the right id.
-- An old binary's MoveTask changes only `board` and leaves board_id stale; the new binary heals
-- that at startup (task.Store.HealTaskBoardIds), the same UPDATE as the defensive pass at the end.
-- The later DROP of `board` must first DROP this DEFAULT: the expression depends on the column.
--
-- IDEMPOTENT: the table is IF NOT EXISTS; the seed is ON DUPLICATE KEY (a no-op on re-run); the
-- column, its index and its FK ride ONE guarded ALTER (a single ALTER is atomic in MySQL 8, so
-- "column without FK" is unreachable; do not split it); the CHECK is resolved by its clause from
-- information_schema (never by the positional task_chk_N name) and dropped only if present.
-- PREPARE/EXECUTE/DEALLOCATE one statement per line: prod runs without multiStatements.
--
-- COST: the expression default forces ALGORITHM=COPY on `task`, a table of hundreds of rows, so the
-- ALTER takes well under a second. No retroactive CHECK is added anywhere.

CREATE TABLE IF NOT EXISTS task_board (
  id INT PRIMARY KEY AUTO_INCREMENT,
  name VARCHAR(64) NOT NULL COMMENT 'display name; unique case-insensitively (utf8mb4_0900_ai_ci)',
  slug VARCHAR(16) NULL COMMENT 'former TaskBoard enum string for the six seeded boards; NULL = custom board',
  position INT NOT NULL DEFAULT 0 COMMENT 'order in the board rail',
  created_by VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'admin account username, from the JWT; empty for seeded boards',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uniq_task_board_name (name),
  UNIQUE KEY uniq_task_board_slug (slug)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Task kanban boards (department lanes)';

INSERT INTO task_board (id, name, slug, position) VALUES
  (1, 'development', 'development', 0),
  (2, 'design', 'design', 1),
  (3, 'marketing', 'marketing', 2),
  (4, 'production', 'production', 3),
  (5, 'sourcing', 'sourcing', 4),
  (6, 'content', 'content', 5)
ON DUPLICATE KEY UPDATE id = id;

SET @task_board_id_col := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'task'
      AND COLUMN_NAME = 'board_id'
);
SET @ddl := IF(@task_board_id_col = 0,
    'ALTER TABLE task ADD COLUMN board_id INT NOT NULL DEFAULT (CASE board WHEN ''development'' THEN 1 WHEN ''design'' THEN 2 WHEN ''marketing'' THEN 3 WHEN ''production'' THEN 4 WHEN ''sourcing'' THEN 5 WHEN ''content'' THEN 6 END) COMMENT ''FK task_board(id); the placement board'' AFTER board, ADD INDEX idx_task_board_id_status_pos (board_id, status, position), ADD CONSTRAINT fk_task_board FOREIGN KEY (board_id) REFERENCES task_board (id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Defensive pass, normally zero rows: re-align board_id with a seeded slug. updated_at is kept,
-- this is a repair, not an edit.
UPDATE task t
JOIN task_board b ON b.slug = t.board
SET t.board_id = b.id, t.updated_at = t.updated_at
WHERE t.board_id <> b.id;

SET @task_board_chk := (SELECT cc.CONSTRAINT_NAME
    FROM information_schema.CHECK_CONSTRAINTS cc
    JOIN information_schema.TABLE_CONSTRAINTS tc
      ON tc.CONSTRAINT_SCHEMA = cc.CONSTRAINT_SCHEMA AND tc.CONSTRAINT_NAME = cc.CONSTRAINT_NAME
    WHERE cc.CONSTRAINT_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'task'
      AND cc.CHECK_CLAUSE LIKE '%board%' AND cc.CHECK_CLAUSE LIKE '%development|design%'
    LIMIT 1);
SET @ddl := IF(@task_board_chk IS NOT NULL, CONCAT('ALTER TABLE task DROP CHECK `', @task_board_chk, '`'), 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- The board CHECK is NOT re-added: rows written as 'custom-<id>' would make it a retroactive CHECK
-- that fails on existing data. Custom-board tasks keep their 'custom-<id>' value, so an Up after
-- this Down fails NOT NULL on them until they are moved to one of the six slugs.

SET @task_board_id_col_down := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'task'
      AND COLUMN_NAME = 'board_id'
);
SET @ddl_down := IF(@task_board_id_col_down = 1,
    'ALTER TABLE task DROP FOREIGN KEY fk_task_board, DROP INDEX idx_task_board_id_status_pos, DROP COLUMN board_id',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

DROP TABLE IF EXISTS task_board;

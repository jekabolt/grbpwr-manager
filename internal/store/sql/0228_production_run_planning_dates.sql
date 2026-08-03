-- +migrate Up

-- Gives a production run the two PLANNING dates the production cockpit needs to answer "what is
-- late?" (production-costing phase 1).
--
-- Until now a run carried only started_at (stamped when work actually began) and received_at
-- (stamped by the receive flow beside the stock it books). Both are FACTS — they say what already
-- happened. Nothing on the row said what was SUPPOSED to happen, so "опаздывает" could not be
-- computed at all: the closest proxy was the stale_open_production_run alert, which measures age
-- since created_at rather than a miss against a commitment.
--
--   planned_start_at -- when the batch is planned to go into work. Client-writable planning intent;
--                       unrelated to started_at, which the operator stamps on the real transition.
--   promised_at      -- дата, к которой партия обещана: the delivery date the run is committed to.
--                       An open run (planned/in_progress) whose promised_at is in the past is
--                       overdue — that is the whole definition the list filter and the tech-card
--                       production tab read.
--
-- Both are NULL-able with no default, so every existing run stays valid and simply reads as
-- "unplanned" — which is the truth about a run booked before this migration. Neither is indexed:
-- production_run is a small table (hundreds of rows), and the overdue predicate is always evaluated
-- together with a status filter that is already selective.
--
-- Idempotent: each ADD COLUMN is guarded by an information_schema check (MySQL 8 has no
-- ADD COLUMN IF NOT EXISTS), so a rerun after a mid-file failure is a no-op.

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'production_run'
      AND COLUMN_NAME = 'planned_start_at'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE production_run ADD COLUMN planned_start_at DATETIME NULL COMMENT ''planned date the batch goes into work (planning intent, not the started_at fact)''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'production_run'
      AND COLUMN_NAME = 'promised_at'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE production_run ADD COLUMN promised_at DATETIME NULL COMMENT ''дата, к которой партия обещана; an open run past it is overdue''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'production_run'
      AND COLUMN_NAME = 'planned_start_at'
);
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE production_run DROP COLUMN planned_start_at', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'production_run'
      AND COLUMN_NAME = 'promised_at'
);
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE production_run DROP COLUMN promised_at', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

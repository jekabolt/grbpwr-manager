-- +migrate Up

-- production-costing Phase 5 (plan 05): partial receipts introduce the 'partially_received' run
-- status (at least one receipt booked, run not yet declared complete). 0233 already widened the
-- column to VARCHAR(24) and parked the value set under the STABLE constraint name exactly so this
-- migration can extend it by name — no information_schema hunt for an auto-named CHECK needed.

SET @have := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run' AND CONSTRAINT_NAME = 'chk_production_run_status');
SET @sql := IF(@have > 0, 'ALTER TABLE production_run DROP CHECK chk_production_run_status', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run' AND CONSTRAINT_NAME = 'chk_production_run_status');
SET @sql := IF(@have > 0, 'SELECT 1',
    'ALTER TABLE production_run ADD CONSTRAINT chk_production_run_status CHECK (status REGEXP ''^(planned|in_progress|partially_received|received|closed|cancelled)$'')');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- Narrowing the value set back would fail on any run already partially received. Intentional no-op.
SELECT 1;

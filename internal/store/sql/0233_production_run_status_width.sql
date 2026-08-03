-- +migrate Up

-- production-costing Phase 4 (plan file 05, amendment 5): widen production_run.status ahead of
-- Phase 5's 'partially_received' (19 chars), which does not fit VARCHAR(16). Widening is decoupled
-- from introducing the value so the Phase 5 deploy window never has a binary writing a status the
-- column truncates. The 0097 CHECK on status is AUTO-NAMED (production_run_chk_N — positional,
-- drifts across schema history, must never be dropped by literal name): resolve its real name via
-- information_schema, drop it dynamically, and re-add the SAME value set under a stable name that
-- Phase 5 can drop and extend by name. PREPARE/EXECUTE one statement per line (no multiStatements
-- on prod), 0106/0107 pattern.

-- --- widen the column (guarded: only if still narrower than 24) ---
SET @need_widen := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run' AND COLUMN_NAME = 'status'
      AND CHARACTER_MAXIMUM_LENGTH < 24);
SET @sql := IF(@need_widen,
    'ALTER TABLE production_run MODIFY COLUMN status VARCHAR(24) NOT NULL DEFAULT ''planned''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- --- drop the auto-named status CHECK, whatever it is currently called ---
SET @cname := (
    SELECT tc.CONSTRAINT_NAME
    FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
        ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'production_run'
        AND tc.CONSTRAINT_TYPE = 'CHECK'
        AND cc.CHECK_CLAUSE LIKE '%status%'
        AND tc.CONSTRAINT_NAME <> 'chk_production_run_status'
    LIMIT 1);
SET @sql := IF(@cname IS NULL, 'SELECT 1', CONCAT('ALTER TABLE production_run DROP CHECK ', @cname));
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- --- re-add under a stable name, same value set (partially_received itself is Phase 5) ---
SET @have := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run' AND CONSTRAINT_NAME = 'chk_production_run_status');
SET @sql := IF(@have > 0, 'SELECT 1',
    'ALTER TABLE production_run ADD CONSTRAINT chk_production_run_status CHECK (status REGEXP ''^(planned|in_progress|received|closed|cancelled)$'')');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- The widened column and the named CHECK are strictly more permissive than / equivalent to 0097's;
-- narrowing back would risk truncating data. Intentional no-op.
SELECT 1;

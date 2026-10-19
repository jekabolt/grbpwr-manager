-- +migrate Up
-- A fitting change request may have no category yet: the fitting screen adds the edit first and
-- the category is picked later (target = ''). 0102 created the target CHECK without a name, so on
-- every database it is some auto-generated fitting_change_request_chk_<n>, and <n> is positional.
-- It is therefore found by its clause, never by a literal name, dropped, and replaced by the named
-- chk_fcr_target_v2 that also accepts ''. Widening a CHECK cannot fail on existing data: every
-- stored value is from the old set. entity.ValidFittingChangeTargets + '' is the Go mirror.
--
-- Crash-idempotent: each step is guarded, so a crash between the two statements re-runs cleanly.

SET @old := (SELECT MIN(tc.CONSTRAINT_NAME)
    FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'fitting_change_request'
      AND tc.CONSTRAINT_TYPE = 'CHECK' AND tc.CONSTRAINT_NAME <> 'chk_fcr_target_v2'
      AND cc.CHECK_CLAUSE LIKE '%`target`%');
SET @sql := IF(@old IS NULL,
    'SELECT 1',
    CONCAT('ALTER TABLE fitting_change_request DROP CHECK `', @old, '`'));
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting_change_request' AND CONSTRAINT_NAME = 'chk_fcr_target_v2');
SET @sql := IF(@has = 0,
    'ALTER TABLE fitting_change_request ADD CONSTRAINT chk_fcr_target_v2 CHECK (target REGEXP ''^(pattern|construction|material|grading|other)?$'')',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
SET @has := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting_change_request' AND CONSTRAINT_NAME = 'chk_fcr_target_v2');
SET @sql := IF(@has,
    'ALTER TABLE fitting_change_request DROP CHECK chk_fcr_target_v2',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Rows without a category would violate the narrow CHECK, so they become «other» before it returns —
-- a down-migration must not leave the table unable to satisfy its own CHECK.
UPDATE fitting_change_request SET target = 'other' WHERE target = '';

SET @has := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting_change_request' AND CONSTRAINT_NAME = 'chk_fcr_target');
SET @sql := IF(@has = 0,
    'ALTER TABLE fitting_change_request ADD CONSTRAINT chk_fcr_target CHECK (target REGEXP ''^(pattern|construction|material|grading|other)$'')',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

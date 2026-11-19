-- +migrate Up

-- join_pending marks an admins row staged by a team invite that has not been finished yet: the row
-- is disabled and carries the invite's rights. Finishing the join is one compare-and-set
-- (disabled = FALSE, join_pending = FALSE WHERE join_pending = TRUE), and any explicit disable of
-- the row clears the marker in the same write, so a late or repeated finish can never re-enable a
-- row an admin or a removal disabled. Existing rows get FALSE: nothing changes for them.
--
-- MySQL auto-commits DDL; the ALTER runs only while the column is missing, so a retry is safe.

SET @admins_has_join_pending := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'admins' AND COLUMN_NAME = 'join_pending');
SET @ddl := IF(@admins_has_join_pending = 0,
    'ALTER TABLE admins ADD COLUMN join_pending BOOLEAN NOT NULL DEFAULT FALSE',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @admins_has_join_pending := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'admins' AND COLUMN_NAME = 'join_pending');
SET @ddl := IF(@admins_has_join_pending = 1,
    'ALTER TABLE admins DROP COLUMN join_pending',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

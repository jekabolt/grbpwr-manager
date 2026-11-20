-- +migrate Up

-- A library file may sit in one library folder (media-files wave, D4); NULL = no folder.
--
-- Plain folder_id, not lib_folder_id: library_file is read as lf.* and joined only by fileslibrary,
-- so there is no m.*-style collision to avoid here.
-- WHY NO FK: with foreign_key_checks=1 adding a FK to an existing table forces ALGORITHM=COPY, and it
-- would be a retroactive constraint. The library store enforces integrity instead (folder delete
-- refuses while any row points at it; moves take the folder FOR SHARE).
--
-- ALGORITHM=INSTANT is explicit on the column; the index is INPLACE, LOCK=NONE.
-- IDEMPOTENCY: DDL auto-commits, so the column and the index each have their own gate.

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file' AND COLUMN_NAME = 'folder_id');
SET @ddl := IF(@col = 0,
    'ALTER TABLE library_file
        ADD COLUMN folder_id INT NULL,
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file' AND INDEX_NAME = 'idx_library_file_folder');
SET @ddl := IF(@idx = 0,
    'ALTER TABLE library_file
        ADD INDEX idx_library_file_folder (folder_id, created_at, id),
        ALGORITHM=INPLACE, LOCK=NONE',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file' AND INDEX_NAME = 'idx_library_file_folder');
SET @ddl := IF(@idx > 0,
    'ALTER TABLE library_file DROP INDEX idx_library_file_folder, ALGORITHM=INPLACE, LOCK=NONE',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file' AND COLUMN_NAME = 'folder_id');
SET @ddl := IF(@col = 1,
    'ALTER TABLE library_file DROP COLUMN folder_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

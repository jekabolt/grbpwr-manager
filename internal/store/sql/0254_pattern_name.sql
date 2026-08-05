-- +migrate Up

-- Gives a pattern file (выкройка) an optional operator-entered display name, on both tables that
-- store pattern rows -- tech_card_size_pattern (final per-size sheets) and fitting_pattern
-- (iterations tried in a fitting). Until now the only label was the factory filename; with several
-- sheets per size the operator needs a human name ("перед", "спинка", "рукав x2").
--
-- NULLable so every pre-existing row stays valid -- an unnamed pattern reads as no name, and the
-- UI falls back to the filename. Both tables are full-replace children of their parent save; the
-- store carries a name forward when a stale client omits the field (see insertTechCardPatterns).
--
-- Idempotent -- each ADD COLUMN is guarded by an information_schema check (MySQL 8 has no
-- ADD COLUMN IF NOT EXISTS), so a rerun after a mid-file failure is a no-op.

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_size_pattern'
      AND COLUMN_NAME = 'name'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_size_pattern ADD COLUMN name VARCHAR(255) NULL AFTER filename',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'fitting_pattern'
      AND COLUMN_NAME = 'name'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE fitting_pattern ADD COLUMN name VARCHAR(255) NULL AFTER filename',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- fitting_pattern.content_type has been dead since 0077 -- nothing ever read or wrote it, every
-- row holds the default. 0226 already dropped its tech_card_size_pattern twin; drop this one too,
-- now that the file type officially travels in the url extension. Without the drop every DXF
-- fitting row would sit stamped 'application/pdf' -- dead but actively wrong data.
SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'fitting_pattern'
      AND COLUMN_NAME = 'content_type'
);
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE fitting_pattern DROP COLUMN content_type', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'fitting_pattern'
      AND COLUMN_NAME = 'content_type'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE fitting_pattern ADD COLUMN content_type VARCHAR(64) NOT NULL DEFAULT ''application/pdf'' AFTER size_bytes',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_size_pattern'
      AND COLUMN_NAME = 'name'
);
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE tech_card_size_pattern DROP COLUMN name', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'fitting_pattern'
      AND COLUMN_NAME = 'name'
);
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE fitting_pattern DROP COLUMN name', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Flat route, stale details (tmp/plans/flat-consistency 82-INPUT-REDESIGN §5, owner 06.10): a flat
-- DETAIL whose plate is older than the run of the card's current front/back flat is «stale»; a person
-- may KEEP it, and the mark is on the server so everyone sees it.
--
-- The mark is stored against the views run AND the detail plate it was made on: it holds only while
-- both are still current, so it clears by itself when the views are drawn again or the detail is
-- replaced — no writer has to remember to clear it, and the old binary (which never writes these
-- columns) cannot leave a wrong mark behind.
--
-- Four nullable / defaulted columns at the end of the table: INSTANT, no row rewritten, no CHECK, no
-- FK; the old binary's `SELECT *` reads them into nothing (the store handle is Unsafe).
-- IDEMPOTENT: information_schema guard, one statement per PREPARE (prod has no multiStatements).

-- +migrate Up

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_bench_slot' AND COLUMN_NAME = 'kept_run_id');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE design_bench_slot ADD COLUMN kept_run_id INT NULL COMMENT ''the views run (design_run id of the front/back flat plate) the stale detail was kept against; NULL = not kept''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_bench_slot' AND COLUMN_NAME = 'kept_picture_id');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE design_bench_slot ADD COLUMN kept_picture_id INT UNSIGNED NULL COMMENT ''the detail plate that was kept; the mark holds only while the slot still holds it''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_bench_slot' AND COLUMN_NAME = 'kept_by');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE design_bench_slot ADD COLUMN kept_by VARCHAR(255) NOT NULL DEFAULT '''' COMMENT ''username who kept the stale detail''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_bench_slot' AND COLUMN_NAME = 'kept_at');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE design_bench_slot ADD COLUMN kept_at DATETIME(6) NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_bench_slot' AND COLUMN_NAME = 'kept_at');
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE design_bench_slot DROP COLUMN kept_at',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_bench_slot' AND COLUMN_NAME = 'kept_by');
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE design_bench_slot DROP COLUMN kept_by',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_bench_slot' AND COLUMN_NAME = 'kept_picture_id');
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE design_bench_slot DROP COLUMN kept_picture_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_bench_slot' AND COLUMN_NAME = 'kept_run_id');
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE design_bench_slot DROP COLUMN kept_run_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Moodboard as the one source of a flat's inputs (tmp/plans/flat-consistency/101-MOODBOARD-ROLES.md,
-- owner 06.10, wave 11): a design_reference row becomes the SERVER'S LABEL on a board picture — which
-- view of the garment it shows, or which detail slot it belongs to — set by a model or a person.
--
-- design_reference gains the label's provenance and state:
--   label_source     — human | model_cheap | model_strong | quiz; '' = a row older than the column (a
--                      person's — every existing row was set by a person);
--   label_state      — pending | ok | unsure | failed; DEFAULT 'ok' so every existing row stays live;
--   proposed_purpose — the model's proposal for a picture's board purpose (the client applies it to an
--                      EMPTY purpose of the form row once; the server never writes tech_card_media.role);
--   model_caption    — what the model read; NEVER sent to a prompt (101 §2.7);
--   label_model      — the slug that answered; labelled_at — when; label_attempts — lazy re-queue cap.
-- design_bench_slot gains made_by_model — a detail slot a model minted from a detail photo; only such a
-- slot may the server delete by itself (empty, its last photo gone).
--
-- Nullable / defaulted columns at the end of the tables: INSTANT, no row rewritten, no CHECK (a
-- retroactive CHECK halts the deploy), no FK; the vocabulary is enforced in Go. The old binary's
-- `SELECT *` reads them into nothing (the store handle is Unsafe).
-- IDEMPOTENT: information_schema guard, one statement per PREPARE (prod has no multiStatements).

-- +migrate Up

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference' AND COLUMN_NAME = 'label_source');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE design_reference ADD COLUMN label_source VARCHAR(16) NOT NULL DEFAULT '''' COMMENT ''who set the view/detail: human|model_cheap|model_strong|quiz; empty = older than the column (human)''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference' AND COLUMN_NAME = 'label_state');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE design_reference ADD COLUMN label_state VARCHAR(16) NOT NULL DEFAULT ''ok'' COMMENT ''pending|ok|unsure|failed; only ok travels to a run''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference' AND COLUMN_NAME = 'proposed_purpose');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE design_reference ADD COLUMN proposed_purpose VARCHAR(16) NOT NULL DEFAULT '''' COMMENT ''the model proposal for the board purpose: target|detail|mood|material''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference' AND COLUMN_NAME = 'model_caption');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE design_reference ADD COLUMN model_caption TEXT NULL COMMENT ''what the model read; never sent to a prompt''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference' AND COLUMN_NAME = 'label_model');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE design_reference ADD COLUMN label_model VARCHAR(96) NOT NULL DEFAULT '''' COMMENT ''the model slug that set the label''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference' AND COLUMN_NAME = 'labelled_at');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE design_reference ADD COLUMN labelled_at DATETIME(6) NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference' AND COLUMN_NAME = 'label_attempts');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE design_reference ADD COLUMN label_attempts TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT ''model attempts of a pending label; failed after 2''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_bench_slot' AND COLUMN_NAME = 'made_by_model');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE design_bench_slot ADD COLUMN made_by_model TINYINT(1) NOT NULL DEFAULT 0 COMMENT ''a detail slot a model minted from a board detail photo''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_bench_slot' AND COLUMN_NAME = 'made_by_model');
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE design_bench_slot DROP COLUMN made_by_model', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference' AND COLUMN_NAME = 'label_attempts');
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE design_reference DROP COLUMN label_attempts', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference' AND COLUMN_NAME = 'labelled_at');
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE design_reference DROP COLUMN labelled_at', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference' AND COLUMN_NAME = 'label_model');
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE design_reference DROP COLUMN label_model', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference' AND COLUMN_NAME = 'model_caption');
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE design_reference DROP COLUMN model_caption', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference' AND COLUMN_NAME = 'proposed_purpose');
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE design_reference DROP COLUMN proposed_purpose', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference' AND COLUMN_NAME = 'label_state');
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE design_reference DROP COLUMN label_state', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference' AND COLUMN_NAME = 'label_source');
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE design_reference DROP COLUMN label_source', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

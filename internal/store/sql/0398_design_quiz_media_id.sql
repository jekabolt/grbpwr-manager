-- +migrate Up

-- Moodboard quiz per-picture questions (96-PICTURE-QUESTIONS): an answer row may be about one
-- moodboard picture — media_id = the tech card media id the question anchored to; 0 = not a picture
-- question (every row saved before this migration).
--
-- A new column with a DEFAULT only — no CHECK (a retroactive CHECK halts the deploy), no FK (the
-- picture may be detached later; the answer stays and the prompt says the picture was removed).
-- Idempotent: guard by information_schema, one statement per PREPARE (prod has no multiStatements).

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_design_quiz_answer'
      AND COLUMN_NAME = 'media_id'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_design_quiz_answer ADD COLUMN media_id INT NOT NULL DEFAULT 0 COMMENT ''moodboard picture (tech card media id) the question is about; 0 = none'' AFTER decision_key',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_design_quiz_answer'
      AND COLUMN_NAME = 'media_id'
);
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card_design_quiz_answer DROP COLUMN media_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

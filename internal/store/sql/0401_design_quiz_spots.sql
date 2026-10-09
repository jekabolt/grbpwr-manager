-- +migrate Up

-- Moodboard quiz spots (99-SPOTS §3): a picture question (media_id, 0398) may carry 1–3 places IN
-- that picture it is about, drawn as numbered rings on the board tile. Stored with the answer as a
-- JSON list of {label, x, y, scale} (x, y in 0..1000 across the picture); NULL = no spots (a
-- whole-picture question, a non-picture question, or a row saved before 0401).
--
-- New column NULL only — no CHECK (a retroactive CHECK halts the deploy), no FK.
-- Idempotent: guard by information_schema, one statement per PREPARE (prod has no multiStatements).

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_design_quiz_answer'
      AND COLUMN_NAME = 'spots_json'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_design_quiz_answer ADD COLUMN spots_json JSON NULL COMMENT ''places in the question picture: [{label, x, y, scale}], x/y 0..1000; NULL = none'' AFTER facts_json',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_design_quiz_answer'
      AND COLUMN_NAME = 'spots_json'
);
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card_design_quiz_answer DROP COLUMN spots_json',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

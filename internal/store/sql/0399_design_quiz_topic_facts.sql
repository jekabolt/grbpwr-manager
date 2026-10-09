-- +migrate Up

-- Moodboard quiz per-topic staleness (98-STALE §1): each answer depends on ONE topic of card facts
-- (fit, materials, construction, design, picture) and stores that topic's facts AT ANSWER TIME as an
-- ordered JSON list of {label, value}. Stale = the topic's facts now differ from the snapshot.
-- Rows saved before this migration keep topic '' and facts_json NULL: staleness falls back to the
-- whole-card card_fingerprint (0392), which is still written.
--
-- New columns with a DEFAULT / NULL only — no CHECK (a retroactive CHECK halts the deploy), no FK.
-- Idempotent: guard by information_schema, one statement per PREPARE (prod has no multiStatements).

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_design_quiz_answer'
      AND COLUMN_NAME = 'topic'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_design_quiz_answer ADD COLUMN topic VARCHAR(16) NOT NULL DEFAULT '''' COMMENT ''the card-fact topic the answer depends on (fit, materials, construction, design, picture); empty = before per-topic tracking'' AFTER card_fingerprint',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_design_quiz_answer'
      AND COLUMN_NAME = 'facts_json'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_design_quiz_answer ADD COLUMN facts_json JSON NULL COMMENT ''the topic facts at answer time: [{label, value}]; NULL = before per-topic tracking'' AFTER topic',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_design_quiz_answer'
      AND COLUMN_NAME = 'facts_json'
);
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card_design_quiz_answer DROP COLUMN facts_json',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_design_quiz_answer'
      AND COLUMN_NAME = 'topic'
);
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card_design_quiz_answer DROP COLUMN topic',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

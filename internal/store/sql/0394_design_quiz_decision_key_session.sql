-- +migrate Up

-- Moodboard quiz E1 + E2 (64-DEFERRED).
--
-- E1 decision_key: every answer row remembers the snake_case key of the DECISION it settles
-- (chest_room, collar_type, …), not the wording. A later quiz drops a question whose key is already
-- answered; a save whose key matches a different stored question id forgets the older row (Go).
-- '' = no key (rows saved before this migration): no dedupe, no supersession.
--
-- E2 tech_card_design_quiz_session: the last generated question list of a card, so a quiz resumes on
-- another tab or device. One OPEN session per card (closed_at IS NULL) — enforced in Go: generating
-- a new one closes the previous inside the card-row lock. Pending = questions minus saved ids.
--
-- A new column with a DEFAULT and a new table only — no CHECK (a retroactive CHECK halts the deploy).
-- Idempotent: guard by information_schema, one statement per PREPARE (prod has no multiStatements).

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_design_quiz_answer'
      AND COLUMN_NAME = 'decision_key'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_design_quiz_answer ADD COLUMN decision_key VARCHAR(64) NOT NULL DEFAULT '''' COMMENT ''snake_case key of the decision; empty = none (pre-0394)'' AFTER question_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

CREATE TABLE IF NOT EXISTS tech_card_design_quiz_session (
    id INT NOT NULL PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT NOT NULL,
    family VARCHAR(16) NOT NULL DEFAULT '' COMMENT 'garment pictogram family of the generated list',
    questions_json JSON NOT NULL COMMENT 'the generated questions, canonical (as returned to the client)',
    created_by VARCHAR(255) NOT NULL DEFAULT '',
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    closed_at DATETIME(6) NULL DEFAULT NULL COMMENT 'NULL = the open session (one per card, Go)',
    KEY idx_tc_design_quiz_session_card (tech_card_id, closed_at),
    CONSTRAINT fk_tc_design_quiz_session_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Moodboard quiz sessions: the generated list, resumable across devices';

-- +migrate Down

DROP TABLE IF EXISTS tech_card_design_quiz_session;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_design_quiz_answer'
      AND COLUMN_NAME = 'decision_key'
);
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card_design_quiz_answer DROP COLUMN decision_key',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

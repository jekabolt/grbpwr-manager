-- +migrate Up

-- Moodboard quiz staleness (62-DEEP-FIXES D1): every answer row remembers the fingerprint of the
-- card's STRUCTURED inputs at the moment it was saved (category, fit, gender, detail rows, BOM
-- names/compositions, base-size measurements — computed in Go, designQuizCardFingerprint). On read
-- an answer whose fingerprint differs from the card's current one is STALE: downstream prompts list
-- it as unconfirmed and the current card facts win.
--
-- '' = saved before this migration (or imported from an archive): counts as FRESH, so no existing
-- answer flips to stale at deploy. ADD COLUMN with a DEFAULT only — no CHECK (a retroactive CHECK
-- halts the deploy).
--
-- Idempotent: guard by information_schema, one statement per PREPARE (prod has no multiStatements).

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_design_quiz_answer'
      AND COLUMN_NAME = 'card_fingerprint'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_design_quiz_answer ADD COLUMN card_fingerprint CHAR(64) NOT NULL DEFAULT '''' COMMENT ''sha256 of the card structured inputs at save; empty = fresh (pre-0392)'' AFTER skipped',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_design_quiz_answer'
      AND COLUMN_NAME = 'card_fingerprint'
);
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card_design_quiz_answer DROP COLUMN card_fingerprint',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

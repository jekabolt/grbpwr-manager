-- +migrate Up

-- Gives a style the drop date it is being made for (production-costing phase 1).
--
-- The card already carries approved_at / released_at — server-stamped facts about the card's own
-- workflow. Neither says when the style is meant to reach the shop, so the production cockpit had no
-- anchor to measure a batch's promised_at against, and "до дропа N дней" could not be shown at all.
--
--   target_drop_date -- the collection drop this style is planned for. Planning intent, hand-set by
--                       the owner; DATE (not DATETIME) because a drop is a calendar day, never a
--                       time of day.
--
-- NULL-able with no default: an existing card reads as "no drop planned", which is the truth about
-- it. Not indexed — tech_card is small and the field is read per card, never scanned.
--
-- Idempotent: the ADD COLUMN is guarded by an information_schema check (MySQL 8 has no
-- ADD COLUMN IF NOT EXISTS), so a rerun after a mid-file failure is a no-op.

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'target_drop_date'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card ADD COLUMN target_drop_date DATE NULL COMMENT ''calendar day this style is planned to drop; planning intent, owner-set''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'target_drop_date'
);
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE tech_card DROP COLUMN target_drop_date', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

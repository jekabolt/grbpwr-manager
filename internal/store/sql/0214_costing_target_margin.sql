-- +migrate Up

-- Gives the costing sheet a target to measure itself against (PLM UI gap 10).
--
-- Nothing in the contract carried a target margin -- not the tech card, not settings, not
-- StyleEconomics -- so the admin's costing waterfall compared the achieved margin against a hard-coded
-- 65% written into the client. A number that decides whether a style is viable should not live in a
-- frontend constant.
--
-- TWO LEVELS, because both questions are real:
--   * tech_card_costing.target_margin_pct -- this style's own target. A basic that lives for years and
--     a one-off collaboration piece are not held to the same number. NULL = no style-specific target.
--   * alert_setting `target_margin_pct` -- the house default, used when the style sets none. It goes in
--     alert_setting because that is the existing generic numeric settings table (0089) and a margin
--     below target is exactly the kind of thing it already holds thresholds for.
--
-- The read path resolves the two into TechCardCosting.effective_target_margin_pct, so the costing tab
-- gets the number it should use in the same (costing:read-gated) response instead of making a second,
-- analytics-gated settings call.
--
-- 65.0 is seeded as the house default: it is the number the admin was already using, so this migration
-- changes no displayed figure -- it only moves where that figure comes from.
--
-- Idempotent: information_schema guard on the column, INSERT IGNORE on the setting (PK setting_key).

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_costing'
      AND COLUMN_NAME = 'target_margin_pct'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_costing ADD COLUMN target_margin_pct DECIMAL(6,2) NULL COMMENT ''gross margin % this style targets; NULL = use the house default''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @chk_exists := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_costing'
      AND CONSTRAINT_NAME = 'chk_tech_card_costing_target_margin'
);
SET @ddl := IF(@chk_exists = 0,
    'ALTER TABLE tech_card_costing ADD CONSTRAINT chk_tech_card_costing_target_margin CHECK (target_margin_pct IS NULL OR (target_margin_pct >= 0 AND target_margin_pct <= 100))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

INSERT IGNORE INTO alert_setting (setting_key, value) VALUES ('target_margin_pct', 65.0);

-- +migrate Down
DELETE FROM alert_setting WHERE setting_key = 'target_margin_pct';

SET @chk_exists := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_costing'
      AND CONSTRAINT_NAME = 'chk_tech_card_costing_target_margin'
);
SET @ddl := IF(@chk_exists = 1,
    'ALTER TABLE tech_card_costing DROP CHECK chk_tech_card_costing_target_margin',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_costing'
      AND COLUMN_NAME = 'target_margin_pct'
);
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE tech_card_costing DROP COLUMN target_margin_pct', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Up
-- X6: what happened to each platform letter after Resend took it, and the addresses we never write
-- to again (a permanent bounce, a "this is spam" click). Idempotent and ANSI_QUOTES-safe: single
-- quotes only (doubled inside dynamic SQL), information_schema guards, CREATE TABLE IF NOT EXISTS.
-- Online: five nullable columns (instant), four secondary indexes on a small table, one new table.

SET @has_col := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_email' AND COLUMN_NAME = 'provider_id'
);
SET @ddl := IF(@has_col = 0,
  'ALTER TABLE platform_email ADD COLUMN provider_id VARCHAR(64) NULL, ADD COLUMN delivery VARCHAR(16) NULL, ADD COLUMN delivery_at DATETIME(3) NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Which team and which invite a letter belongs to (invite letters only): the per-team invite cap
-- and an invite's «bounced» read only that team's / that invite's own letters.
SET @has_col := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_email' AND COLUMN_NAME = 'team_id'
);
SET @ddl := IF(@has_col = 0,
  'ALTER TABLE platform_email ADD COLUMN team_id BIGINT NULL, ADD COLUMN invite_id BIGINT NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_idx := (
  SELECT COUNT(*) FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_email' AND INDEX_NAME = 'idx_platform_email_team_created'
);
SET @ddl := IF(@has_idx = 0,
  'ALTER TABLE platform_email ADD INDEX idx_platform_email_team_created (team_id, created_at)',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_idx := (
  SELECT COUNT(*) FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_email' AND INDEX_NAME = 'idx_platform_email_invite'
);
SET @ddl := IF(@has_idx = 0,
  'ALTER TABLE platform_email ADD INDEX idx_platform_email_invite (invite_id)',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_idx := (
  SELECT COUNT(*) FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_email' AND INDEX_NAME = 'idx_platform_email_provider'
);
SET @ddl := IF(@has_idx = 0,
  'ALTER TABLE platform_email ADD INDEX idx_platform_email_provider (provider_id)',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_idx := (
  SELECT COUNT(*) FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_email' AND INDEX_NAME = 'idx_platform_email_template_created'
);
SET @ddl := IF(@has_idx = 0,
  'ALTER TABLE platform_email ADD INDEX idx_platform_email_template_created (template, created_at)',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

CREATE TABLE IF NOT EXISTS platform_mail_suppression (
  email VARCHAR(254) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  reason VARCHAR(16) NOT NULL,
  first_at DATETIME(3) NOT NULL,
  last_at DATETIME(3) NOT NULL,
  events INT NOT NULL DEFAULT 1,
  PRIMARY KEY (email)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- +migrate Down
-- Added columns and tables are never dropped by a migration.

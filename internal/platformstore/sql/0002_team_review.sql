-- +migrate Up
-- Sign-up approval and terms (02-DESIGN §21). A confirmed sign-up waits in 'review' until an
-- operator approves it; operators get a 'team_request' letter, the owner a 'team_approved' one.
-- platform_user remembers which terms version an account accepted and when. Idempotent: MODIFY
-- of an ENUM re-runs as a no-op, each ADD COLUMN is guarded by information_schema.

ALTER TABLE team MODIFY status ENUM('pending','review','provisioning','active','suspended','deleted') NOT NULL;

ALTER TABLE platform_email MODIFY template ENUM('verify','invite','reset','team_request','team_approved') NOT NULL;

SET @has_terms_version := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_user' AND COLUMN_NAME = 'terms_version'
);
SET @ddl := IF(@has_terms_version = 0,
  'ALTER TABLE platform_user ADD COLUMN terms_version VARCHAR(32) NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_terms_accepted_at := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_user' AND COLUMN_NAME = 'terms_accepted_at'
);
SET @ddl := IF(@has_terms_accepted_at = 0,
  'ALTER TABLE platform_user ADD COLUMN terms_accepted_at DATETIME NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
-- Widened ENUMs and added columns are never narrowed or dropped by a migration.

-- +migrate Up
-- Sign-up approval and terms (02-DESIGN §21). A confirmed sign-up waits in 'review' until an
-- operator approves it; operators get a 'team_request' letter, the owner a 'team_approved' one.
-- platform_user remembers which terms version an account accepted and when; platform_email gets a
-- lease and a stored retry time for the mail worker. Idempotent: MODIFY
-- of an ENUM re-runs as a no-op, each ADD COLUMN is guarded by information_schema.

ALTER TABLE team MODIFY status ENUM('pending','review','provisioning','active','suspended','deleted') NOT NULL;

ALTER TABLE platform_email MODIFY template ENUM('verify','invite','reset','team_request','team_approved') NOT NULL;

-- An invite is accepted in two commits around the tenant admins write: 'joining' is the reserved,
-- not yet active membership between them (sign-in and hand-off read only 'active').
ALTER TABLE team_member MODIFY status ENUM('active','removed','joining') NOT NULL;

-- enable_pending: the join wrote the member's admins row DISABLED and has not enabled it yet. Once
-- the membership is active, the join's last step (or the member's next platform sign-in to the
-- team) enables the row and clears the flag; a disabled row without the flag stays disabled.
SET @has_enable_pending := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'team_member' AND COLUMN_NAME = 'enable_pending'
);
SET @ddl := IF(@has_enable_pending = 0,
  'ALTER TABLE team_member ADD COLUMN enable_pending TINYINT(1) NOT NULL DEFAULT 0',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

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

-- The mail worker leases a row before calling Resend (lease_until) and keeps the retry time in the
-- row (next_attempt_at), so a send holds no transaction and restarts or replicas share the backoff.
SET @has_next_attempt := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_email' AND COLUMN_NAME = 'next_attempt_at'
);
SET @ddl := IF(@has_next_attempt = 0,
  'ALTER TABLE platform_email ADD COLUMN next_attempt_at DATETIME(3) NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_lease := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_email' AND COLUMN_NAME = 'lease_until'
);
SET @ddl := IF(@has_lease = 0,
  'ALTER TABLE platform_email ADD COLUMN lease_until DATETIME(3) NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
-- Widened ENUMs and added columns are never narrowed or dropped by a migration.

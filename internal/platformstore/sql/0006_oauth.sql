-- +migrate Up
-- Sign-in with Google / Apple, the root list of teams, root log out, team logo (teams O).
-- Idempotent and ANSI_QUOTES-safe: single quotes only (doubled inside dynamic SQL), CREATE TABLE
-- IF NOT EXISTS, the widened ENUM re-runs as a no-op, each ADD COLUMN is guarded by
-- information_schema. Online on live tables: three new tables, one ENUM member appended at the
-- end (instant), nullable columns (instant). No new index: the root sign-out filters by user_id
-- (idx_user_session_user, idx_email_token_user).

-- One provider identity of an account: (provider, subject) is the key; email is what the provider
-- said at link time and never rewrites platform_user.email.
CREATE TABLE IF NOT EXISTS platform_identity (
  id BIGINT NOT NULL AUTO_INCREMENT,
  user_id BIGINT NOT NULL,
  provider ENUM('google','apple') NOT NULL,
  subject VARCHAR(255) NOT NULL,
  email VARCHAR(254) NULL,
  private_email TINYINT(1) NOT NULL DEFAULT 0,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  last_login_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_platform_identity_subject (provider, subject),
  KEY idx_platform_identity_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- One provider flow in flight: single use, bound to the HttpOnly cookie by binder_hash (sha256).
-- Targets are signin and invite only; the row holds no team name or address.
CREATE TABLE IF NOT EXISTS oauth_state (
  state_hash CHAR(64) NOT NULL,
  binder_hash CHAR(64) NOT NULL,
  provider ENUM('google','apple') NOT NULL,
  target ENUM('signin','invite') NOT NULL,
  pkce_verifier VARCHAR(128) NOT NULL,
  nonce VARCHAR(64) NOT NULL,
  invite_hash CHAR(64) NULL,
  terms_version VARCHAR(32) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  expires_at DATETIME(3) NOT NULL,
  used_at DATETIME(3) NULL,
  PRIMARY KEY (state_hash),
  KEY idx_oauth_state_expires (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- A team's logo, fitted into 256x256 and re-encoded as PNG; sha256 is the ETag and the ?v= of
-- the logo URL. Lives in the control plane, so it works before the team is provisioned.
CREATE TABLE IF NOT EXISTS team_logo (
  team_id BIGINT NOT NULL,
  png MEDIUMBLOB NOT NULL,
  sha256 CHAR(64) NOT NULL,
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (team_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- The 60-second one-time code a provider callback hands to the root page.
ALTER TABLE email_token MODIFY purpose ENUM('verify','reset','handoff','signin') NOT NULL;

-- parent_sid: the root session a hand-off code / team session was opened from, so a root
-- sign-out ends them too. NULL for everything opened without a root session.
SET @has_token_parent := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_token' AND COLUMN_NAME = 'parent_sid'
);
SET @ddl := IF(@has_token_parent = 0,
  'ALTER TABLE email_token ADD COLUMN parent_sid CHAR(32) NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_session_parent := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'user_session' AND COLUMN_NAME = 'parent_sid'
);
SET @ddl := IF(@has_session_parent = 0,
  'ALTER TABLE user_session ADD COLUMN parent_sid CHAR(32) NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
-- Never dropped: identities, sessions and logos outlive a binary rollback (as 0001-0005).

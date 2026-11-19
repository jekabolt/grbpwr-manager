-- +migrate Up
-- Account profile, sessions list, Google connect and delete with 30 days to undo (teams P0 §2).
-- Idempotent and ANSI_QUOTES-safe: single quotes only (doubled inside dynamic SQL), CREATE TABLE
-- IF NOT EXISTS, each widened ENUM restates the full current list and appends at the end (re-runs
-- as a no-op), each ADD COLUMN / ADD KEY is guarded by information_schema. Online on live tables:
-- appended ENUM members and nullable or defaulted columns at the end are instant; the three new
-- keys are built in place. No backfill: every new column defaults to nothing set (empty names,
-- public_username NULL = the handle, entry_pending 0, all dates NULL). No FK, as every
-- control-plane table.

-- Status 'deleted': an account inside its 30-day window (deleted_at, purge_at set); after the
-- purge it stays 'deleted' as a tombstone with purged_at set.
ALTER TABLE platform_user MODIFY status ENUM('pending','active','disabled','deleted') NOT NULL;

-- 'frozen': a membership of a deleted account, back to 'active' on restore, 'removed' at the purge.
ALTER TABLE team_member MODIFY status ENUM('active','removed','joining','frozen') NOT NULL;

-- 'review': a team in review that closed together with its only member's account.
ALTER TABLE team MODIFY deleted_from ENUM('active','suspended','review') NULL;

-- 'restore': the email code that brings a deleted account back.
ALTER TABLE email_token MODIFY purpose ENUM('verify','reset','handoff','signin','restore') NOT NULL;

ALTER TABLE platform_email MODIFY template ENUM('verify','invite','reset','team_request','team_approved','restore','account_deleted') NOT NULL;

-- 'link': a provider flow that attaches Google to a signed-in account.
ALTER TABLE oauth_state MODIFY target ENUM('signin','invite','link') NOT NULL;

-- platform_user: first / last name, the mutable @username (NULL = same as the handle), when the
-- password last changed, and the delete window (deleted_at, purge_at, purged_at).
SET @has_first_name := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_user' AND COLUMN_NAME = 'first_name'
);
SET @ddl := IF(@has_first_name = 0,
  'ALTER TABLE platform_user ADD COLUMN first_name VARCHAR(60) NOT NULL DEFAULT ''''',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_last_name := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_user' AND COLUMN_NAME = 'last_name'
);
SET @ddl := IF(@has_last_name = 0,
  'ALTER TABLE platform_user ADD COLUMN last_name VARCHAR(60) NOT NULL DEFAULT ''''',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_public_username := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_user' AND COLUMN_NAME = 'public_username'
);
SET @ddl := IF(@has_public_username = 0,
  'ALTER TABLE platform_user ADD COLUMN public_username VARCHAR(30) NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_password_changed_at := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_user' AND COLUMN_NAME = 'password_changed_at'
);
SET @ddl := IF(@has_password_changed_at = 0,
  'ALTER TABLE platform_user ADD COLUMN password_changed_at DATETIME(3) NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_user_deleted_at := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_user' AND COLUMN_NAME = 'deleted_at'
);
SET @ddl := IF(@has_user_deleted_at = 0,
  'ALTER TABLE platform_user ADD COLUMN deleted_at DATETIME(3) NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_user_purge_at := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_user' AND COLUMN_NAME = 'purge_at'
);
SET @ddl := IF(@has_user_purge_at = 0,
  'ALTER TABLE platform_user ADD COLUMN purge_at DATETIME(3) NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_user_purged_at := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_user' AND COLUMN_NAME = 'purged_at'
);
SET @ddl := IF(@has_user_purged_at = 0,
  'ALTER TABLE platform_user ADD COLUMN purged_at DATETIME(3) NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- One namespace for @usernames: the key settles a race between two renames (NULLs never collide).
SET @has_public_username_key := (
  SELECT COUNT(*) FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_user' AND INDEX_NAME = 'uq_platform_user_public_username'
);
SET @ddl := IF(@has_public_username_key = 0,
  'ALTER TABLE platform_user ADD UNIQUE KEY uq_platform_user_public_username (public_username)',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- The purge pass reads only accounts inside their window.
SET @has_user_purge_key := (
  SELECT COUNT(*) FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_user' AND INDEX_NAME = 'idx_platform_user_purge'
);
SET @ddl := IF(@has_user_purge_key = 0,
  'ALTER TABLE platform_user ADD KEY idx_platform_user_purge (purge_at)',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- user_session: the device a session was opened from, recorded at creation.
SET @has_session_user_agent := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'user_session' AND COLUMN_NAME = 'user_agent'
);
SET @ddl := IF(@has_session_user_agent = 0,
  'ALTER TABLE user_session ADD COLUMN user_agent VARCHAR(255) NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_session_ip := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'user_session' AND COLUMN_NAME = 'ip'
);
SET @ddl := IF(@has_session_ip = 0,
  'ALTER TABLE user_session ADD COLUMN ip VARCHAR(45) NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- team_member: 1 while a new membership has not passed the first-entry screen. Existing rows get 0.
SET @has_entry_pending := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'team_member' AND COLUMN_NAME = 'entry_pending'
);
SET @ddl := IF(@has_entry_pending = 0,
  'ALTER TABLE team_member ADD COLUMN entry_pending TINYINT(1) NOT NULL DEFAULT 0',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- team_invite: the name the inviter typed; it seeds a new account on accept.
SET @has_invite_name := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'team_invite' AND COLUMN_NAME = 'name'
);
SET @ddl := IF(@has_invite_name = 0,
  'ALTER TABLE team_invite ADD COLUMN name VARCHAR(120) NOT NULL DEFAULT ''''',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- oauth_state: the account a 'link' flow attaches Google to, and the team it returns to.
SET @has_link_user := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'oauth_state' AND COLUMN_NAME = 'link_user_id'
);
SET @ddl := IF(@has_link_user = 0,
  'ALTER TABLE oauth_state ADD COLUMN link_user_id BIGINT NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_state_return_slug := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'oauth_state' AND COLUMN_NAME = 'return_slug'
);
SET @ddl := IF(@has_state_return_slug = 0,
  'ALTER TABLE oauth_state ADD COLUMN return_slug VARCHAR(63) NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- team: the account a team closed together with (its only member); restore brings it back only
-- with that account. NULL on every team not closed that way.
SET @has_closed_with := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'team' AND COLUMN_NAME = 'closed_with_user_id'
);
SET @ddl := IF(@has_closed_with = 0,
  'ALTER TABLE team ADD COLUMN closed_with_user_id BIGINT NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_closed_with_key := (
  SELECT COUNT(*) FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'team' AND INDEX_NAME = 'idx_team_closed_with'
);
SET @ddl := IF(@has_closed_with_key = 0,
  'ALTER TABLE team ADD KEY idx_team_closed_with (closed_with_user_id)',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- An account's photo, fitted into 256x256 and re-encoded as PNG (as team_logo). Served by sha256,
-- so the URL carries no id or handle.
CREATE TABLE IF NOT EXISTS account_avatar (
  user_id BIGINT NOT NULL,
  png MEDIUMBLOB NOT NULL,
  sha256 CHAR(64) NOT NULL,
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (user_id),
  KEY idx_account_avatar_sha (sha256)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- A Google-connect ticket: single use, short-lived, stored hashed, bound to the account and the
-- team it returns to.
CREATE TABLE IF NOT EXISTS oauth_link_ticket (
  ticket_hash CHAR(64) NOT NULL,
  user_id BIGINT NOT NULL,
  return_slug VARCHAR(63) NULL,
  expires_at DATETIME(3) NOT NULL,
  used_at DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (ticket_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- +migrate Down
-- Never dropped or narrowed: profiles, sessions, deleted accounts and their windows outlive a
-- binary rollback (as 0001-0008).

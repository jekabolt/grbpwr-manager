-- +migrate Up
-- Control plane of lekalo.studio (02-DESIGN §2): users, teams, membership, invites, email tokens,
-- sessions, per-team feature overrides, slug history, spare databases, outgoing mail, AI spend,
-- audit. All times DATETIME(3) UTC. Idempotent: every table IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS platform_user (
  id BIGINT NOT NULL AUTO_INCREMENT,
  email VARCHAR(254) NULL,
  handle VARCHAR(64) NOT NULL,
  password_hash VARCHAR(255) NOT NULL,
  email_verified_at DATETIME(3) NULL,
  is_operator TINYINT(1) NOT NULL DEFAULT 0,
  status ENUM('pending','active','disabled') NOT NULL,
  code_failures SMALLINT NOT NULL DEFAULT 0,
  code_failures_since DATETIME(3) NULL,
  code_locked_until DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_platform_user_email (email),
  UNIQUE KEY uq_platform_user_handle (handle)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS team (
  id BIGINT NOT NULL AUTO_INCREMENT,
  slug VARCHAR(40) NOT NULL,
  name VARCHAR(120) NOT NULL,
  db_name VARCHAR(64) NULL,
  file_key CHAR(32) NULL,
  profile ENUM('standard','all') NOT NULL DEFAULT 'standard',
  status ENUM('pending','provisioning','active','suspended','deleted') NOT NULL,
  schema_version VARCHAR(128) NULL,
  schema_fingerprint CHAR(64) NULL,
  pending_expires_at DATETIME(3) NULL,
  created_by BIGINT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  activated_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_team_slug (slug),
  UNIQUE KEY uq_team_db_name (db_name),
  UNIQUE KEY uq_team_file_key (file_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS team_member (
  team_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  role ENUM('owner','member') NOT NULL,
  status ENUM('active','removed') NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (team_id, user_id),
  KEY idx_team_member_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS team_invite (
  id BIGINT NOT NULL AUTO_INCREMENT,
  team_id BIGINT NOT NULL,
  email VARCHAR(254) NOT NULL,
  role ENUM('owner','member') NOT NULL,
  permissions JSON NOT NULL,
  token_hash CHAR(64) NOT NULL,
  invited_by BIGINT NOT NULL,
  expires_at DATETIME(3) NOT NULL,
  accepted_at DATETIME(3) NULL,
  revoked_at DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_team_invite_token (token_hash),
  KEY idx_team_invite_team (team_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS email_token (
  id BIGINT NOT NULL AUTO_INCREMENT,
  user_id BIGINT NOT NULL,
  team_id BIGINT NULL,
  purpose ENUM('verify','reset','handoff') NOT NULL,
  token_hash CHAR(64) NOT NULL,
  code_hash CHAR(64) NULL,
  code_attempts TINYINT NOT NULL DEFAULT 0,
  expires_at DATETIME(3) NOT NULL,
  used_at DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_email_token_hash (token_hash),
  KEY idx_email_token_user (user_id, purpose, used_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS user_session (
  sid CHAR(32) NOT NULL,
  user_id BIGINT NOT NULL,
  team_id BIGINT NULL,
  aud ENUM('team','console','root') NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  last_seen_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  revoked_at DATETIME(3) NULL,
  PRIMARY KEY (sid),
  KEY idx_user_session_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS team_feature (
  team_id BIGINT NOT NULL,
  feature VARCHAR(40) NOT NULL,
  enabled TINYINT(1) NOT NULL,
  updated_by BIGINT NULL,
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (team_id, feature)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS team_slug_history (
  slug VARCHAR(40) NOT NULL,
  team_id BIGINT NOT NULL,
  retired_at DATETIME(3) NOT NULL,
  redirect_until DATETIME(3) NOT NULL,
  PRIMARY KEY (slug),
  KEY idx_team_slug_history_team (team_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS spare_db (
  db_name VARCHAR(64) NOT NULL,
  status ENUM('creating','ready','failed','claimed') NOT NULL,
  schema_version VARCHAR(128) NULL,
  schema_fingerprint CHAR(64) NULL,
  error TEXT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  claimed_team_id BIGINT NULL,
  claimed_at DATETIME(3) NULL,
  PRIMARY KEY (db_name),
  KEY idx_spare_db_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS platform_email (
  id BIGINT NOT NULL AUTO_INCREMENT,
  to_email VARCHAR(254) NOT NULL,
  template ENUM('verify','invite','reset') NOT NULL,
  payload JSON NOT NULL,
  status ENUM('pending','sent','skipped','failed') NOT NULL,
  attempts INT NOT NULL DEFAULT 0,
  last_error TEXT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  sent_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  KEY idx_platform_email_status (status, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS team_ai_spend_daily (
  team_id BIGINT NOT NULL,
  day DATE NOT NULL,
  cost_usd DECIMAL(14,6) NOT NULL,
  unpriced_calls INT NOT NULL,
  calls INT NOT NULL,
  refreshed_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (team_id, day)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS platform_audit (
  id BIGINT NOT NULL AUTO_INCREMENT,
  actor_user_id BIGINT NULL,
  team_id BIGINT NULL,
  action VARCHAR(64) NOT NULL,
  detail JSON NULL,
  ip VARCHAR(45) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_platform_audit_team (team_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- +migrate Down
-- Control plane tables are never dropped by a migration.


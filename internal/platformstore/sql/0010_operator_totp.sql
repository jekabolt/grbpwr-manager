-- +migrate Up
-- Operator console second step (wave teams, gap 4): a code from an authenticator app (TOTP) after the
-- password. Idempotent and ANSI_QUOTES-safe: single quotes only, CREATE TABLE IF NOT EXISTS, the
-- widened ENUM restates the full current list and appends at the end (re-runs as a no-op). Online:
-- three new empty tables, one appended ENUM member pair (instant) and operators signed out. No FK, as every control-plane
-- table. No backfill: an operator without a row is made to set the app up on the next sign-in.

-- operator_totp: the confirmed authenticator of one operator. secret_sealed is AES-256-GCM under a
-- key derived from PLATFORM_TOKEN_PEPPER (never the clear secret). last_step is the last accepted
-- 30-second step (a code is never accepted twice). failed_count / locked_until: wrong codes in a row
-- and the lockout they cause.
CREATE TABLE IF NOT EXISTS operator_totp (
  user_id BIGINT NOT NULL,
  secret_sealed VARBINARY(255) NOT NULL,
  confirmed_at DATETIME(3) NOT NULL,
  last_step BIGINT NOT NULL DEFAULT 0,
  failed_count INT NOT NULL DEFAULT 0,
  locked_until DATETIME(3) NULL,
  PRIMARY KEY (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- operator_recovery_code: one-time codes shown once at setup; stored as HMAC under the pepper.
CREATE TABLE IF NOT EXISTS operator_recovery_code (
  id BIGINT NOT NULL AUTO_INCREMENT,
  user_id BIGINT NOT NULL,
  code_hash CHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  used_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_operator_recovery_code (user_id, code_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- operator_second_step: the short-lived ticket between a right password and the console token.
-- kind 'code': the operator has an app and types its code. kind 'setup': no app yet — a code was
-- mailed (mail_code_hash) and must be typed first (mail_ok_at), then the pending secret is confirmed
-- with an app code. cred_gen pins the password the ticket was earned with.
CREATE TABLE IF NOT EXISTS operator_second_step (
  ticket_hash CHAR(64) NOT NULL,
  user_id BIGINT NOT NULL,
  cred_gen BIGINT NOT NULL,
  kind ENUM('code','setup') NOT NULL,
  mail_code_hash CHAR(64) NULL,
  mail_ok_at DATETIME(3) NULL,
  pending_secret_sealed VARBINARY(255) NULL,
  attempts INT NOT NULL DEFAULT 0,
  created_at DATETIME(3) NOT NULL,
  expires_at DATETIME(3) NOT NULL,
  used_at DATETIME(3) NULL,
  PRIMARY KEY (ticket_hash),
  KEY idx_operator_second_step_user (user_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- operator_setup: the mailed code before a first setup. operator_alert: the second step was set up,
-- reset, or a recovery code was used — sent to every operator address.
ALTER TABLE platform_email MODIFY template ENUM('verify','invite','reset','team_request','team_approved','restore','account_deleted','operator_setup','operator_alert') NOT NULL;

-- Sessions from before the second step: every console session and the operator team sessions
-- opened from it end now (a console token without the step is refused anyway; this also ends the
-- team sessions an old console token opened). Re-run after a mid-file failure only signs operators
-- out once more.
UPDATE user_session SET revoked_at = UTC_TIMESTAMP(3)
WHERE revoked_at IS NULL AND (aud = 'console'
  OR parent_sid IN (SELECT c.sid FROM (SELECT sid FROM user_session WHERE aud = 'console') c));
-- +migrate Down
-- Never dropped or narrowed: an operator's enrolled app outlives a binary rollback (as 0001-0009).

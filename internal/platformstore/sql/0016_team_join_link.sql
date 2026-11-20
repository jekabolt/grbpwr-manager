-- +migrate Up
-- One re-copyable team join link, its moderated requests, and the pending sign-up intent.
-- Idempotent and ANSI_QUOTES-safe: tables use IF NOT EXISTS and string literals use single quotes.

CREATE TABLE IF NOT EXISTS team_join_link (
  team_id BIGINT NOT NULL,
  gen BIGINT NOT NULL DEFAULT 1,
  nonce BINARY(16) NOT NULL,
  rotated_at DATETIME(3) NOT NULL,
  rotated_by BIGINT NOT NULL,
  PRIMARY KEY (team_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS team_join_request (
  id BIGINT NOT NULL AUTO_INCREMENT,
  team_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  gen BIGINT NOT NULL,
  status ENUM('pending','approved','declined','expired','closed') NOT NULL DEFAULT 'pending',
  ip VARCHAR(45) NULL,
  decided_at DATETIME(3) NULL,
  decided_by BIGINT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  open_user_id BIGINT GENERATED ALWAYS AS (IF(status = 'pending', user_id, NULL)) STORED,
  PRIMARY KEY (id),
  UNIQUE KEY uq_team_join_request_open (team_id, open_user_id),
  KEY idx_team_join_request_team_gen (team_id, gen, status, created_at),
  KEY idx_team_join_request_team_created (team_id, created_at),
  KEY idx_team_join_request_user (user_id, status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS platform_join_intent (
  user_id BIGINT NOT NULL,
  team_id BIGINT NOT NULL,
  gen BIGINT NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (user_id),
  KEY idx_platform_join_intent_team (team_id, gen)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

ALTER TABLE email_token MODIFY purpose ENUM('verify','reset','handoff','signin','restore','verify_join') NOT NULL;

ALTER TABLE platform_email MODIFY template ENUM('verify','invite','reset','team_request','team_approved','restore','account_deleted','operator_setup','operator_alert','join_code','join_request','join_approved') NOT NULL;

-- +migrate Down
-- Join rows and widened ENUM members are never dropped by a migration.

-- +migrate Up
-- D3 + S4c: the day-30 wipe of a deleted team and the purge of a deleted account.
-- team_wipe: one row per claimed team — what the claim took off the team row (database name, file
-- key) and how far the wipe got (files, database, finalize), with its retry schedule. Two keys for
-- the account purge: platform_email by recipient, platform_audit by actor.
-- Idempotent and ANSI_QUOTES-safe: single quotes only, CREATE TABLE IF NOT EXISTS, each ADD KEY
-- guarded by information_schema. Online: one new table, two secondary keys (in place, no lock).

CREATE TABLE IF NOT EXISTS team_wipe (
  team_id BIGINT NOT NULL,
  slug VARCHAR(40) NOT NULL,
  db_name VARCHAR(64) NULL,
  file_key CHAR(32) NULL,
  purge_at DATETIME(3) NOT NULL,
  started_at DATETIME(3) NOT NULL,
  files_done_at DATETIME(3) NULL,
  db_dropped_at DATETIME(3) NULL,
  finished_at DATETIME(3) NULL,
  attempts INT NOT NULL DEFAULT 0,
  next_attempt_at DATETIME(3) NOT NULL,
  last_error VARCHAR(500) NULL,
  objects_deleted BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (team_id),
  KEY idx_team_wipe_open (finished_at, next_attempt_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

SET @has_email_to := (
  SELECT COUNT(*) FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_email' AND INDEX_NAME = 'idx_platform_email_to'
);
SET @ddl := IF(@has_email_to = 0,
  'ALTER TABLE platform_email ADD KEY idx_platform_email_to (to_email), ALGORITHM=INPLACE, LOCK=NONE',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_audit_actor := (
  SELECT COUNT(*) FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_audit' AND INDEX_NAME = 'idx_platform_audit_actor'
);
SET @ddl := IF(@has_audit_actor = 0,
  'ALTER TABLE platform_audit ADD KEY idx_platform_audit_actor (actor_user_id), ALGORITHM=INPLACE, LOCK=NONE',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
-- Never dropped: a rollback binary ignores the table and the keys (as 0001-0016).

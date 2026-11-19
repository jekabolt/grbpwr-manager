-- +migrate Up
-- Workspace delete from the console (teams D1). DeleteTeam hides a team at once (status 'deleted')
-- and records when it was deleted, when its data is wiped (purge_at) and the status it had before
-- (deleted_from: 'active' or 'suspended'), which RestoreTeam brings back while purge_at is ahead.
-- All three are NULL on a team that is not deleted. The wipe itself is a later lane.
-- Additive and idempotent: each ADD COLUMN / ADD KEY is guarded by information_schema.

SET @has_deleted_at := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'team' AND COLUMN_NAME = 'deleted_at'
);
SET @ddl := IF(@has_deleted_at = 0,
  'ALTER TABLE team ADD COLUMN deleted_at DATETIME(3) NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_purge_at := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'team' AND COLUMN_NAME = 'purge_at'
);
SET @ddl := IF(@has_purge_at = 0,
  'ALTER TABLE team ADD COLUMN purge_at DATETIME(3) NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_deleted_from := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'team' AND COLUMN_NAME = 'deleted_from'
);
SET @ddl := IF(@has_deleted_from = 0,
  'ALTER TABLE team ADD COLUMN deleted_from ENUM(''active'',''suspended'') NULL',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- DeleteTeam revokes every live session and consumes every unexchanged hand-off of one team inside
-- its transaction: these indexes keep those locking reads on the team's own rows.
SET @has_session_team := (
  SELECT COUNT(*) FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'user_session' AND INDEX_NAME = 'idx_user_session_team'
);
SET @ddl := IF(@has_session_team = 0,
  'ALTER TABLE user_session ADD KEY idx_user_session_team (team_id)',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_token_team := (
  SELECT COUNT(*) FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_token' AND INDEX_NAME = 'idx_email_token_team'
);
SET @ddl := IF(@has_token_team = 0,
  'ALTER TABLE email_token ADD KEY idx_email_token_team (team_id)',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
-- Added columns and keys are never dropped by a migration.

-- +migrate Up
-- team.schema_min_code: the newest breaking team migration applied to the team's database (X3,
-- internal/schemacompat). NULL = none, which is every team until the first breaking migration
-- ships: only a binary that embeds this migration id may serve the team. Raised before a migration
-- run and before activation, never lowered. Idempotent and ANSI_QUOTES-safe (single quotes, the
-- ADD COLUMN guarded by information_schema); online: a nullable column at the end is instant.

SET @has_min_code := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'team' AND COLUMN_NAME = 'schema_min_code'
);
SET @ddl := IF(@has_min_code = 0,
  'ALTER TABLE team ADD COLUMN schema_min_code VARCHAR(128) NULL, ALGORITHM=INSTANT',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
-- Never dropped: a rollback binary ignores the column (as 0001-0009).

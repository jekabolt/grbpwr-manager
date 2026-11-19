-- +migrate Up
-- Credential generation of a platform user. Every password change (reset, admin-panel change, an
-- admin resetting another account, re-creating a team #1 account) bumps it in the same transaction
-- as the hash and the session revoke. Legacy team #1 tokens (the old /api/auth/login, no session
-- id) carry the generation they were issued under; the edge refuses one whose generation is not
-- the current one. A token without the claim counts as generation 0.
-- Additive and idempotent: the ADD COLUMN is guarded by information_schema.

SET @has_credential_gen := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'platform_user' AND COLUMN_NAME = 'credential_gen'
);
SET @ddl := IF(@has_credential_gen = 0,
  'ALTER TABLE platform_user ADD COLUMN credential_gen BIGINT UNSIGNED NOT NULL DEFAULT 0',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
-- Added columns are never dropped by a migration.

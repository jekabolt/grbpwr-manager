-- +migrate Up
-- Legacy team #1 tokens (the old /api/auth/login, no session id) cannot be revoked by row: the edge
-- refuses one whose iat is before the account's last password change instead. Written by the
-- password reset and by the admin-panel password change, in the same transaction as the hash and the
-- session revoke. NULL = never changed through the control plane (no legacy token is refused).
-- Additive and idempotent: the ADD COLUMN is guarded by information_schema.

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

-- +migrate Down
-- Added columns are never dropped by a migration.

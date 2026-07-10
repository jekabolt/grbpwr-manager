-- +migrate Up
-- Description: Role-based access control for admin accounts. Until now every
--   authenticated admin could call any /admin.AdminService/* RPC. This adds
--   per-account, per-section access levels so an account can be scoped to only
--   some parts of the admin panel (e.g. content + hero only, orders read-only).
--   is_super grants full access and bypasses the section checks. disabled blocks
--   an account from obtaining new tokens at login (existing tokens live until the
--   JWT expires — permissions ride inside the token, so authorization stays
--   stateless; a revoke takes effect on the account's next login).
-- Backfill: every existing admin is marked is_super=TRUE so the RBAC rollout does
--   not lock anyone out — scoped accounts are created explicitly afterwards.
-- Affected tables: admins (new columns), admin_permission (new)
-- Type: additive (non-breaking; existing admins keep full access)

ALTER TABLE admins
  ADD COLUMN is_super BOOLEAN NOT NULL DEFAULT FALSE COMMENT 'Full access: bypasses per-section checks and may manage accounts.',
  ADD COLUMN disabled BOOLEAN NOT NULL DEFAULT FALSE COMMENT 'Blocked from logging in (obtaining new tokens); existing tokens live until expiry.',
  ADD COLUMN created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  ADD COLUMN updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP;

-- Existing admins predate RBAC and must retain the unrestricted access they had,
-- otherwise the first deploy of this feature would lock them all out.
UPDATE admins SET is_super = TRUE;

-- One row per (account, section) the account may access. access is 'read' or
-- 'write'; 'write' implies 'read'. Absence of a row means no access to that
-- section. Rows are removed with the account (ON DELETE CASCADE). section is a
-- stable key from the application's section catalog (products, orders, ...).
CREATE TABLE admin_permission (
    admin_id INT NOT NULL,
    section  VARCHAR(64) NOT NULL,
    access   VARCHAR(8)  NOT NULL COMMENT 'read | write (write implies read)',
    PRIMARY KEY (admin_id, section),
    CONSTRAINT fk_admin_permission_admin
        FOREIGN KEY (admin_id) REFERENCES admins (id) ON DELETE CASCADE
);

-- +migrate Down

DROP TABLE admin_permission;

ALTER TABLE admins
  DROP COLUMN is_super,
  DROP COLUMN disabled,
  DROP COLUMN created_at,
  DROP COLUMN updated_at;

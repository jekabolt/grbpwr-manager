-- +migrate Up
-- Provisioning leases (B4 review): one owner per team being provisioned ("team:<id>") and per team
-- or spare database being migrated ("db:<name>"). A lease is taken in one short statement, kept
-- alive by its holder and re-takable once expired; the writes that matter (activate, schema
-- version) join the lease row by token, so a holder that lost its lease writes nothing.
-- The row "spare-quota" is never a lease (its token is empty and it never expires): it is the
-- lock row every spare-database reservation takes, so the hourly cap holds across workers.
-- Idempotent.
CREATE TABLE IF NOT EXISTS provision_lease (
  lease_key VARCHAR(96) NOT NULL,
  token CHAR(32) NOT NULL,
  expires_at DATETIME(3) NOT NULL,
  PRIMARY KEY (lease_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

INSERT IGNORE INTO provision_lease (lease_key, token, expires_at) VALUES ('spare-quota', '', '9999-12-31 00:00:00.000');

-- +migrate Down
-- Control plane tables are never dropped by a migration.

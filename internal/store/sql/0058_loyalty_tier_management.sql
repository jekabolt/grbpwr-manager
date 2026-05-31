-- +migrate Up
-- Loyalty tier management (admin tier & account management, tech spec v1).
-- Adds membership lifecycle to accounts, configurable tier definitions, a tier
-- transition audit log, one-time hacker invites, a qualifying-spend cache,
-- per-product tier gating, and an EUR snapshot on orders so loyalty spend is
-- currency-stable (the shop has no live FX; prices are hand-set per currency).
-- Tier codes: member=0, plus=1, plus_plus=2, hacker=99.

-- 1. Membership lifecycle columns on the existing account table.
ALTER TABLE storefront_account
  ADD COLUMN status ENUM('active','frozen','deleted','erased') NOT NULL DEFAULT 'active' COMMENT 'Account lifecycle status',
  ADD COLUMN tier_upgrade_date DATETIME NULL DEFAULT NULL COMMENT 'When the current tier was reached (anchor for expiration)',
  ADD COLUMN next_review_date DATETIME NULL DEFAULT NULL COMMENT 'When tier maintenance is next evaluated',
  ADD COLUMN deleted_at TIMESTAMP NULL DEFAULT NULL COMMENT 'Soft-delete timestamp',
  ADD INDEX idx_storefront_account_status (status),
  ADD INDEX idx_storefront_account_next_review (next_review_date);

-- 2. Tier configuration (admin-editable thresholds / expiration; numeric codes).
CREATE TABLE tier_config (
  tier_code SMALLINT PRIMARY KEY COMMENT 'numeric tier code: 0/1/2/99',
  tier_key VARCHAR(20) NOT NULL UNIQUE COMMENT 'matches storefront_account.account_tier enum value',
  display_name VARCHAR(50) NOT NULL,
  min_spend_eur DECIMAL(10,2) NULL DEFAULT NULL COMMENT 'EUR spend threshold to qualify; NULL for base / invite-only',
  expiration_days INT NOT NULL DEFAULT 365,
  reminder_days_before INT NOT NULL DEFAULT 30,
  is_invite_only BOOLEAN NOT NULL DEFAULT FALSE,
  welcome_pack_slots INT NULL DEFAULT NULL COMMENT 'remaining welcome-pack slots (e.g. first 100); NULL = not tracked',
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) COMMENT 'Editable loyalty tier definitions';

INSERT INTO tier_config
  (tier_code, tier_key, display_name, min_spend_eur, expiration_days, reminder_days_before, is_invite_only, welcome_pack_slots)
VALUES
  (0,  'member',    'grbpwr',        NULL,    365, 30, FALSE, 100),
  (1,  'plus',      'grbpwr+',       1000.00, 365, 30, FALSE, NULL),
  (2,  'plus_plus', 'grbpwr++',      3000.00, 365, 30, FALSE, NULL),
  (99, 'hacker',    'grbpwr hacker', NULL,    365, 30, TRUE,  NULL);

-- 3. Tier transition audit log (auto + manual).
CREATE TABLE tier_history (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  account_id INT NOT NULL,
  old_tier VARCHAR(20) NOT NULL,
  new_tier VARCHAR(20) NOT NULL,
  trigger_type VARCHAR(32) NOT NULL COMMENT 'upgrade|downgrade|refund_rollback|manual|backfill|hacker_grant|hacker_revoke',
  reason TEXT NULL,
  actor VARCHAR(255) NOT NULL DEFAULT 'system' COMMENT 'admin email or "system"',
  spend_eur_at_change DECIMAL(10,2) NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_tier_history_account (account_id),
  INDEX idx_tier_history_trigger (trigger_type),
  INDEX idx_tier_history_created (created_at),
  FOREIGN KEY (account_id) REFERENCES storefront_account(id) ON DELETE CASCADE
) COMMENT 'Audit log of all tier transitions';

-- 4. Hacker invites (one-time tokens).
CREATE TABLE hacker_invite (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  token_hash CHAR(64) NOT NULL UNIQUE COMMENT 'sha256 of one-time token',
  email VARCHAR(100) NULL DEFAULT NULL COMMENT 'optional pre-bound email',
  created_by VARCHAR(255) NOT NULL COMMENT 'admin email',
  expires_at TIMESTAMP NOT NULL,
  consumed_at TIMESTAMP NULL DEFAULT NULL,
  consumed_by_account_id INT NULL DEFAULT NULL,
  revoked_at TIMESTAMP NULL DEFAULT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_hacker_invite_expires (expires_at),
  INDEX idx_hacker_invite_email (email),
  FOREIGN KEY (consumed_by_account_id) REFERENCES storefront_account(id) ON DELETE SET NULL
) COMMENT 'One-time invite links granting hacker tier';

-- 5. Qualifying-spend cache (denormalized for the members list / detail screens).
CREATE TABLE qualifying_spend_cache (
  account_id INT PRIMARY KEY,
  amount_eur DECIMAL(10,2) NOT NULL DEFAULT 0.00,
  window_start DATETIME NOT NULL,
  window_end DATETIME NOT NULL,
  computed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  FOREIGN KEY (account_id) REFERENCES storefront_account(id) ON DELETE CASCADE
) COMMENT 'Cached rolling 12-month qualifying spend in EUR';

-- 6. Per-product tier gating.
ALTER TABLE product
  ADD COLUMN min_tier SMALLINT NOT NULL DEFAULT 0 COMMENT 'Minimum tier code required to purchase (0/1/2/99)',
  ADD COLUMN hidden_for_non_qualified BOOLEAN NOT NULL DEFAULT FALSE COMMENT 'Hide entirely from non-qualified tiers',
  ADD INDEX idx_product_min_tier (min_tier);

-- 7. EUR snapshot on orders for currency-stable loyalty spend.
ALTER TABLE customer_order
  ADD COLUMN total_price_eur DECIMAL(10,2) NULL DEFAULT NULL COMMENT 'Order total converted to EUR at order time for loyalty spend accumulation';

-- +migrate Down

ALTER TABLE customer_order
  DROP COLUMN total_price_eur;

ALTER TABLE product
  DROP INDEX idx_product_min_tier,
  DROP COLUMN hidden_for_non_qualified,
  DROP COLUMN min_tier;

DROP TABLE IF EXISTS qualifying_spend_cache;
DROP TABLE IF EXISTS hacker_invite;
DROP TABLE IF EXISTS tier_history;
DROP TABLE IF EXISTS tier_config;

ALTER TABLE storefront_account
  DROP INDEX idx_storefront_account_next_review,
  DROP INDEX idx_storefront_account_status,
  DROP COLUMN deleted_at,
  DROP COLUMN next_review_date,
  DROP COLUMN tier_upgrade_date,
  DROP COLUMN status;

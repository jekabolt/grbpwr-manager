-- +migrate Up
-- Shops of teams (X18): the hosts a storefront answers on, a team's shop switch and mail sender, and
-- the shape of a team's Stripe setup (filled by X18-L3). Idempotent and ANSI_QUOTES-safe: tables use
-- IF NOT EXISTS and string literals use single quotes. Online: three new tables, nothing on a live
-- table. No FK, as every control-plane table: deletes are explicit.

-- One row per host (lowercase, no scheme / port / trailing dot). A host belongs to one team.
-- kind: subdomain = {slug}.<shop zone> (platform DNS), custom = the team's own, legacy = grbpwr's.
-- primary_team makes «one main address per team» a unique key.
CREATE TABLE IF NOT EXISTS shop_host (
  host VARCHAR(253) NOT NULL,
  team_id BIGINT NOT NULL,
  kind ENUM('subdomain','custom','legacy') NOT NULL,
  managed TINYINT(1) NOT NULL DEFAULT 0,
  is_primary TINYINT(1) NOT NULL DEFAULT 0,
  status ENUM('records_needed','verifying','live','failed') NOT NULL DEFAULT 'records_needed',
  redirect_to VARCHAR(253) NOT NULL DEFAULT '',
  records JSON NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  verified_at DATETIME(3) NULL,
  checked_at DATETIME(3) NULL,
  primary_team BIGINT GENERATED ALWAYS AS (IF(is_primary = 1, team_id, NULL)) STORED,
  PRIMARY KEY (host),
  UNIQUE KEY uq_shop_host_primary (primary_team),
  KEY idx_shop_host_team (team_id, kind)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- A team's shop: its own on/off, the operator's close, and the sender of its letters.
CREATE TABLE IF NOT EXISTS team_shop (
  team_id BIGINT NOT NULL,
  enabled TINYINT(1) NOT NULL DEFAULT 0,
  enabled_at DATETIME(3) NULL,
  closed_by_operator TINYINT(1) NOT NULL DEFAULT 0,
  mail_from_name VARCHAR(64) NOT NULL DEFAULT '',
  mail_reply_to VARCHAR(254) NOT NULL DEFAULT '',
  updated_by BIGINT NULL,
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (team_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- A team's Stripe: Connect (account_id, direct charges) or its own keys (sealed at rest). Shape only;
-- X18-L3 writes it. Keys are never stored in the clear: *_sealed are AEAD ciphertexts.
CREATE TABLE IF NOT EXISTS team_stripe (
  team_id BIGINT NOT NULL,
  kind ENUM('connect','keys') NOT NULL,
  account_id VARCHAR(64) NULL,
  account_name VARCHAR(128) NOT NULL DEFAULT '',
  mode ENUM('live','test') NOT NULL DEFAULT 'test',
  charges_enabled TINYINT(1) NOT NULL DEFAULT 0,
  payouts_enabled TINYINT(1) NOT NULL DEFAULT 0,
  details_submitted TINYINT(1) NOT NULL DEFAULT 0,
  fee_bps INT NOT NULL DEFAULT 0,
  publishable_key VARCHAR(255) NOT NULL DEFAULT '',
  secret_key_sealed VARBINARY(512) NULL,
  webhook_secret_sealed VARBINARY(512) NULL,
  key_last4 CHAR(4) NOT NULL DEFAULT '',
  connected_at DATETIME(3) NULL,
  disconnected_at DATETIME(3) NULL,
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (team_id),
  UNIQUE KEY uq_team_stripe_account (account_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- +migrate Down
-- Never dropped: shop rows outlive a binary rollback (as 0001-0016).

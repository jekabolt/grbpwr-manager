-- +migrate Up
-- Migration: Expand storefront_account with phone, tier, and marketing preferences
-- Purpose: Add phone number, account tier, and email subscription preferences to customer accounts

ALTER TABLE storefront_account
  ADD COLUMN phone VARCHAR(32) NULL DEFAULT NULL COMMENT 'Customer phone number',
  ADD COLUMN account_tier ENUM('plus', 'plus_plus', 'hacker') NOT NULL DEFAULT 'plus' COMMENT 'Account tier / membership level',
  ADD COLUMN subscribe_newsletter BOOLEAN NOT NULL DEFAULT FALSE COMMENT 'Opt-in for newsletter emails',
  ADD COLUMN subscribe_new_arrivals BOOLEAN NOT NULL DEFAULT FALSE COMMENT 'Opt-in for new arrivals notifications',
  ADD COLUMN subscribe_events BOOLEAN NOT NULL DEFAULT FALSE COMMENT 'Opt-in for event notifications',
  ADD COLUMN default_country VARCHAR(2) NULL DEFAULT NULL COMMENT 'ISO 3166-1 alpha-2 country code',
  ADD COLUMN default_language VARCHAR(2) NULL DEFAULT NULL COMMENT 'ISO 639-1 language code';

-- Backfill NULL shopping_preference values to 'all' before making it NOT NULL
UPDATE storefront_account SET shopping_preference = 'all' WHERE shopping_preference IS NULL;

-- Now make shopping_preference NOT NULL with default
ALTER TABLE storefront_account
  MODIFY COLUMN shopping_preference ENUM('male', 'female', 'all') NOT NULL DEFAULT 'all' COMMENT 'Catalog browsing preference';

-- Backfill subscribe_newsletter from existing subscriber table
UPDATE storefront_account sa
LEFT JOIN subscriber s ON s.email = sa.email
SET sa.subscribe_newsletter = COALESCE(s.receive_promo_emails, 0);

-- +migrate Down
ALTER TABLE storefront_account
  DROP COLUMN default_language,
  DROP COLUMN default_country,
  DROP COLUMN subscribe_events,
  DROP COLUMN subscribe_new_arrivals,
  DROP COLUMN subscribe_newsletter,
  DROP COLUMN account_tier,
  DROP COLUMN phone,
  MODIFY COLUMN shopping_preference ENUM('male', 'female', 'all') NULL DEFAULT NULL;

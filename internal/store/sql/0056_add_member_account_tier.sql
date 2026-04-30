-- +migrate Up
-- Migration: Add baseline account tier `member` and use it as the default for new accounts
-- Purpose: New registrations get `member`; existing rows keep their current tier value

ALTER TABLE storefront_account
  MODIFY COLUMN account_tier ENUM('member', 'plus', 'plus_plus', 'hacker') NOT NULL DEFAULT 'member' COMMENT 'Account tier / membership level';

-- +migrate Down
UPDATE storefront_account SET account_tier = 'plus' WHERE account_tier = 'member';

ALTER TABLE storefront_account
  MODIFY COLUMN account_tier ENUM('plus', 'plus_plus', 'hacker') NOT NULL DEFAULT 'plus' COMMENT 'Account tier / membership level';

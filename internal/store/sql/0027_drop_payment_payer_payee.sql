-- +migrate Up
-- Drop crypto-specific columns from payment table (crypto payments removed)
ALTER TABLE payment DROP COLUMN payer;
ALTER TABLE payment DROP COLUMN payee;

-- +migrate Down
ALTER TABLE payment ADD COLUMN payer VARCHAR(255) NULL;
ALTER TABLE payment ADD COLUMN payee VARCHAR(255) NULL;

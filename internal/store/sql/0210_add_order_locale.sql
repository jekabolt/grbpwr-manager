-- +migrate Up
-- Site locale captured at purchase time (ISO-639-1: en/fr/de/it/ja/zh/ko), so an order's
-- transactional emails can be sent in the language the customer was shopping in when they
-- have no explicit account preference. Nullable: pre-feature orders and admin custom orders
-- leave it NULL, and the mailer falls back to the account language / EN. Additive + idempotent.
ALTER TABLE customer_order ADD COLUMN IF NOT EXISTS locale VARCHAR(8) NULL;

-- +migrate Down
ALTER TABLE customer_order DROP COLUMN IF EXISTS locale;

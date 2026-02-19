-- +migrate Up
-- Add payment sub-type from provider (e.g. card, apple_pay, klarna from Stripe)
ALTER TABLE payment ADD COLUMN payment_method_type VARCHAR(50) NULL;

-- +migrate Down
ALTER TABLE payment DROP COLUMN payment_method_type;

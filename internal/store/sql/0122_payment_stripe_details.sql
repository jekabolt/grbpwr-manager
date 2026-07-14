-- +migrate Up

-- Enrich the payment row with the Stripe payment detail captured from the charge and its
-- balance transaction at confirmation time, so the admin panel can show what was actually
-- paid without re-hitting the Stripe API on every order view:
--   * card_brand / card_last4      — the funding card (support, dispute lookup)
--   * receipt_url                  — Stripe-hosted receipt (also surfaced to the customer)
--   * stripe_exchange_rate         — presentment->settlement FX Stripe applied at the sale;
--                                    pairs with customer_order.total_settled_base to explain a
--                                    non-EUR sale ("$100 -> EUR 91.30 @ 0.9130")
--   * stripe_risk_level            — Stripe Radar outcome (normal/elevated/highest) for review
-- The Stripe PaymentIntent id itself is stored in the existing payment.transaction_id column
-- (previously only populated for manual/custom orders); combined with the payment method it
-- yields the dashboard deep link. All nullable and best-effort: NULL for pre-feature orders,
-- non-Stripe methods, or when the balance transaction was not yet available at capture.
--
-- Idempotent: each ADD COLUMN is guarded via information_schema so a mid-file failure (DDL
-- auto-commits, no gorp_migrations row) re-runs cleanly. Multi-line PREPARE/EXECUTE/DEALLOCATE
-- (a single-line form returns 1064 on the managed DSN).

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'payment' AND COLUMN_NAME = 'card_brand');
SET @sql := IF(@need,
    'ALTER TABLE payment ADD COLUMN card_brand VARCHAR(20) NULL COMMENT ''Funding card brand (visa, mastercard, amex...) from the Stripe charge''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'payment' AND COLUMN_NAME = 'card_last4');
SET @sql := IF(@need,
    'ALTER TABLE payment ADD COLUMN card_last4 VARCHAR(4) NULL COMMENT ''Last 4 digits of the funding card''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'payment' AND COLUMN_NAME = 'receipt_url');
SET @sql := IF(@need,
    'ALTER TABLE payment ADD COLUMN receipt_url VARCHAR(512) NULL COMMENT ''Stripe-hosted receipt URL for the charge''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'payment' AND COLUMN_NAME = 'stripe_exchange_rate');
SET @sql := IF(@need,
    'ALTER TABLE payment ADD COLUMN stripe_exchange_rate DECIMAL(18, 8) NULL COMMENT ''Presentment->settlement FX rate Stripe applied at the sale''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'payment' AND COLUMN_NAME = 'stripe_risk_level');
SET @sql := IF(@need,
    'ALTER TABLE payment ADD COLUMN stripe_risk_level VARCHAR(20) NULL COMMENT ''Stripe Radar risk level (normal/elevated/highest)''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

SET @has := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'payment' AND COLUMN_NAME = 'card_brand');
SET @sql := IF(@has,
    'ALTER TABLE payment DROP COLUMN card_brand, DROP COLUMN card_last4, DROP COLUMN receipt_url, DROP COLUMN stripe_exchange_rate, DROP COLUMN stripe_risk_level',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

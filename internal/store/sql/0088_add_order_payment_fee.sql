-- +migrate Up
-- Store the Stripe processing fee for an order in the base currency (EUR), taken from the
-- charge's balance transaction (BalanceTransaction.Fee) at capture time — the same source
-- and FX as total_settled_base. Payment fees are a real contribution-margin cost that gross
-- margin ignores (the documented 2-3pp leak). NULL = not captured (orders placed before this
-- feature, non-Stripe payment methods, or still-unpaid orders). Stripe keeps the processing
-- fee even when an order is refunded, so this is the full fee actually paid, NOT refund-adjusted.
ALTER TABLE customer_order
  ADD COLUMN payment_fee DECIMAL(10, 2) NULL DEFAULT NULL
    COMMENT 'Stripe processing fee in base currency (EUR) from the charge balance transaction; NULL = not captured';

-- +migrate Down
ALTER TABLE customer_order
  DROP COLUMN payment_fee;

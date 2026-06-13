-- +migrate Up
-- Store the actual amount Stripe settled for an order, converted to the base
-- currency (EUR) at Stripe's FX rate (from the charge's balance transaction).
-- This is the authoritative base-currency revenue figure; revenue metrics use it
-- in preference to reconstructing from the mutable product_price table. NULL means
-- not captured (orders placed before this feature, non-Stripe payment methods, or
-- still-unpaid orders), in which case metrics fall back to the reconstruction.
ALTER TABLE customer_order
  ADD COLUMN total_settled_base DECIMAL(10, 2) NULL DEFAULT NULL
    COMMENT 'Actual Stripe-settled amount in base currency (EUR) at Stripe FX; NULL = not captured';

-- +migrate Down
ALTER TABLE customer_order
  DROP COLUMN total_settled_base;

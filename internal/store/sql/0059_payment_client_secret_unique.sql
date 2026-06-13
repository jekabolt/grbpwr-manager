-- +migrate Up
-- Enforce one order per Stripe PaymentIntent. The application stores the
-- PaymentIntent reference in payment.client_secret; a UNIQUE index makes the
-- concurrent-SubmitOrder race fail fast (the losing AssociatePaymentIntentWithOrder
-- hits a duplicate-key error) instead of silently creating duplicate orders with
-- double stock reduction. NULL is allowed for many rows (MySQL permits multiple
-- NULLs in a UNIQUE index), so Placed and custom (bank_invoice/cash) orders whose
-- client_secret is not yet set are unaffected.

ALTER TABLE payment
  ADD UNIQUE INDEX uniq_payment_client_secret (client_secret);

-- +migrate Down

ALTER TABLE payment
  DROP INDEX uniq_payment_client_secret;

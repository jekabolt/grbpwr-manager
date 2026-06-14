-- +migrate Up
-- Enforce one order per Stripe PaymentIntent. The application stores the
-- PaymentIntent reference in payment.client_secret; a UNIQUE index makes the
-- concurrent-SubmitOrder race fail fast (the losing AssociatePaymentIntentWithOrder
-- hits a duplicate-key error) instead of silently creating duplicate orders with
-- double stock reduction. NULL is allowed for many rows (MySQL permits multiple
-- NULLs in a UNIQUE index), so Placed and custom (bank_invoice/cash) orders whose
-- client_secret is not yet set are unaffected.

-- Historical data may contain duplicate client_secret values (a PaymentIntent
-- associated with more than one order before this constraint existed). Collapse
-- each duplicate group to a single owner before adding the index: keep the
-- completed payment (or, failing that, the most recent row) and NULL the rest.
-- A PaymentIntent can only really settle one order, so the others are spurious;
-- multiple NULLs are allowed by the unique index.
UPDATE payment p
JOIN (
  SELECT id FROM (
    SELECT id,
           ROW_NUMBER() OVER (
             PARTITION BY client_secret
             ORDER BY is_transaction_done DESC, id DESC
           ) AS rn
    FROM payment
    WHERE client_secret IS NOT NULL AND client_secret <> ''
  ) ranked
  WHERE ranked.rn > 1
) dups ON p.id = dups.id
SET p.client_secret = NULL;

ALTER TABLE payment
  ADD UNIQUE INDEX uniq_payment_client_secret (client_secret);

-- +migrate Down

ALTER TABLE payment
  DROP INDEX uniq_payment_client_secret;

-- +migrate Up
-- Description: Structured refund reason code alongside the free-text refund_reason. Lets the
--   admin refund form submit a canonical reason (RefundReason enum) so the return-analysis
--   breakdown is exact instead of fuzzy string-matching. NULL for historical refunds (they
--   fall back to normalizing the free-text refund_reason). Non-breaking: refund_reason stays.
-- Affected tables: customer_order
-- Type: additive (non-breaking)

ALTER TABLE customer_order
  ADD COLUMN refund_reason_code VARCHAR(32) NULL DEFAULT NULL COMMENT 'Canonical refund reason key (wrong_size, not_as_described, defective, changed_mind, other); NULL = legacy free-text only.';

-- +migrate Down

ALTER TABLE customer_order
  DROP COLUMN refund_reason_code;

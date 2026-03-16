-- +migrate Up
-- Migration: Rename stock change sources, add order_comment

-- Rename order_reserved -> order_paid (idempotent: only matches old names)
UPDATE product_stock_change_history SET source = 'order_paid' WHERE source = 'order_reserved';

-- Rename order_custom_reserved -> order_custom
UPDATE product_stock_change_history SET source = 'order_custom' WHERE source = 'order_custom_reserved';

-- Backfill reason for order_paid entries
UPDATE product_stock_change_history SET reason = 'order' WHERE source = 'order_paid' AND (reason IS NULL OR reason = '');

-- Backfill reason for order_custom entries
UPDATE product_stock_change_history SET reason = 'custom_order' WHERE source = 'order_custom' AND (reason IS NULL OR reason = '');

-- Add order_comment column
ALTER TABLE product_stock_change_history
  ADD COLUMN order_comment TEXT NULL
    COMMENT 'Order comment from customer_order for order-related stock changes';

-- Backfill order_comment from existing orders
UPDATE product_stock_change_history psch
  JOIN customer_order co ON co.id = psch.order_id
  SET psch.order_comment = co.order_comment
  WHERE psch.order_id IS NOT NULL
    AND co.order_comment IS NOT NULL
    AND co.order_comment != '';

-- +migrate Down
UPDATE product_stock_change_history SET source = 'order_reserved' WHERE source = 'order_paid';
UPDATE product_stock_change_history SET source = 'order_custom_reserved' WHERE source = 'order_custom';

ALTER TABLE product_stock_change_history
  DROP COLUMN order_comment;
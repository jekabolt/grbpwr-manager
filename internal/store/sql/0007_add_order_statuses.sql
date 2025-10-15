-- +migrate Up
-- Add new order statuses: pending_return and refund_in_progress
INSERT INTO order_status (name) VALUES
  ('pending_return'),
  ('refund_in_progress');

-- Add refund_reason column to customer_order table
ALTER TABLE customer_order
ADD COLUMN refund_reason TEXT NULL 
COMMENT 'Reason provided by customer for cancellation or refund request';

-- Add order_comment column to customer_order table
ALTER TABLE customer_order
ADD COLUMN order_comment TEXT NULL 
COMMENT 'Admin comment for the order';

-- +migrate Down
-- Remove order_comment column from customer_order table
ALTER TABLE customer_order
DROP COLUMN order_comment;

-- Remove refund_reason column from customer_order table
ALTER TABLE customer_order
DROP COLUMN refund_reason;

-- Remove the new order statuses
DELETE FROM order_status 
WHERE name IN ('pending_return', 'refund_in_progress');

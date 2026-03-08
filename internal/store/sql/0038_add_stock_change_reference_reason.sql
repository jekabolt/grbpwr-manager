-- +migrate Up
-- Migration: Add reference tracking, reason enum, and comment to stock change history
-- Purpose: Enable detailed tracking of all stock adjustments with reasons and references

-- Add reference_id for transaction tracking
ALTER TABLE product_stock_change_history
ADD COLUMN reference_id VARCHAR(100) NULL 
COMMENT 'Generic reference: adjustment ID, transfer ID, receiving ID, order UUID, etc.';

-- Add reason enum (required for manual adjustments, optional for orders)
ALTER TABLE product_stock_change_history
ADD COLUMN reason VARCHAR(50) NULL 
COMMENT 'Reason for stock change: damaged, lost, found, restock, inventory_correction, return_defective, theft';

-- Add optional comment for additional context
ALTER TABLE product_stock_change_history
ADD COLUMN comment TEXT NULL 
COMMENT 'Optional free-text comment providing additional context for the stock change';

-- Add indexes for filtering
ALTER TABLE product_stock_change_history
ADD INDEX idx_reference_id (reference_id);

ALTER TABLE product_stock_change_history
ADD INDEX idx_reason (reason);

-- Backfill reference_id for existing records
-- For order-related changes: use order_uuid
UPDATE product_stock_change_history
SET reference_id = order_uuid
WHERE order_uuid IS NOT NULL 
  AND order_uuid != ''
  AND reference_id IS NULL;

-- For admin changes with username: use 'admin:{username}' format
UPDATE product_stock_change_history
SET reference_id = CONCAT('admin:', admin_username)
WHERE admin_username IS NOT NULL 
  AND admin_username != ''
  AND reference_id IS NULL;

-- For admin changes without username: use 'system:auto'
UPDATE product_stock_change_history
SET reference_id = 'system:auto'
WHERE reference_id IS NULL;

-- Backfill reason for admin-related historical changes
-- Set default reason to 'inventory_correction' for all admin adjustments
UPDATE product_stock_change_history
SET reason = 'inventory_correction'
WHERE source IN ('admin_add_product', 'admin_update_product', 'admin_update_size_stock')
  AND reason IS NULL;

-- +migrate Down
-- Remove indexes
ALTER TABLE product_stock_change_history
DROP INDEX idx_reference_id;

ALTER TABLE product_stock_change_history
DROP INDEX idx_reason;

-- Remove columns
ALTER TABLE product_stock_change_history
DROP COLUMN reference_id;

ALTER TABLE product_stock_change_history
DROP COLUMN reason;

ALTER TABLE product_stock_change_history
DROP COLUMN comment;

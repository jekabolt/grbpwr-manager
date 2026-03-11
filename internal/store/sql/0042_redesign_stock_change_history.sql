-- +migrate Up
-- Migration: Redesign stock change history with new sources, reasons, and financial columns

-- Add financial columns
ALTER TABLE product_stock_change_history
  ADD COLUMN price_before_discount DECIMAL(15,2) NULL
    COMMENT 'Item price before discount in payment currency (per-item for order items, shipping cost for SHIPPING)',
  ADD COLUMN discount_amount DECIMAL(15,2) NULL
    COMMENT 'Discount amount in payment currency (promo code or manual discount)',
  ADD COLUMN paid_currency VARCHAR(3) NULL
    COMMENT 'ISO 4217 currency code of payment',
  ADD COLUMN paid_amount DECIMAL(15,2) NULL
    COMMENT 'Amount paid in payment currency',
  ADD COLUMN payout_base_amount DECIMAL(15,2) NULL
    COMMENT 'Payout amount in base currency (EUR) after Stripe conversion',
  ADD COLUMN payout_base_currency VARCHAR(3) NULL DEFAULT 'EUR'
    COMMENT 'Base currency for payout (always EUR)';

-- Make product_id and size_id nullable for SHIPPING entries
ALTER TABLE product_stock_change_history
  MODIFY COLUMN product_id INT NULL,
  MODIFY COLUMN size_id INT NULL;

-- Backfill: rename old sources to new sources
UPDATE product_stock_change_history SET source = 'admin_new_product' WHERE source = 'admin_add_product';
UPDATE product_stock_change_history SET source = 'manual_adjustment' WHERE source IN ('admin_update_product', 'admin_update_size_stock');
UPDATE product_stock_change_history SET source = 'order_reserved' WHERE source = 'order_placed';
UPDATE product_stock_change_history SET source = 'order_returned' WHERE source = 'order_refunded';
-- order_cancelled stays as-is
-- Remove order_expired entries (stock was restored, these are noise)
DELETE FROM product_stock_change_history WHERE source = 'order_expired';
-- Clean up unused sources
DELETE FROM product_stock_change_history WHERE source IN ('receiving', 'transfer_in', 'transfer_out', 'damage', 'loss');

-- Backfill reasons for existing records
UPDATE product_stock_change_history SET reason = 'initial_stock' WHERE source = 'admin_new_product' AND (reason IS NULL OR reason = '' OR reason = 'inventory_correction');
UPDATE product_stock_change_history SET reason = 'order' WHERE source = 'order_reserved' AND (reason IS NULL OR reason = '');
UPDATE product_stock_change_history SET reason = 'return_to_stock' WHERE source = 'order_returned' AND (reason IS NULL OR reason = '');
UPDATE product_stock_change_history SET reason = 'order_cancelled' WHERE source = 'order_cancelled' AND (reason IS NULL OR reason = '');

-- +migrate Down
-- Reverse source renames
UPDATE product_stock_change_history SET source = 'admin_add_product' WHERE source = 'admin_new_product';
UPDATE product_stock_change_history SET source = 'order_placed' WHERE source = 'order_reserved';
UPDATE product_stock_change_history SET source = 'order_refunded' WHERE source = 'order_returned';

-- Make product_id and size_id NOT NULL again (remove SHIPPING entries first)
DELETE FROM product_stock_change_history WHERE product_id IS NULL OR size_id IS NULL;
ALTER TABLE product_stock_change_history
  MODIFY COLUMN product_id INT NOT NULL,
  MODIFY COLUMN size_id INT NOT NULL;

-- Drop financial columns
ALTER TABLE product_stock_change_history
  DROP COLUMN price_before_discount,
  DROP COLUMN discount_amount,
  DROP COLUMN paid_currency,
  DROP COLUMN paid_amount,
  DROP COLUMN payout_base_amount,
  DROP COLUMN payout_base_currency;
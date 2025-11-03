-- +migrate Up
-- Add soft delete support to product table
ALTER TABLE product 
ADD COLUMN deleted_at TIMESTAMP NULL DEFAULT NULL COMMENT 'Timestamp when product was soft deleted';

-- Create an index on deleted_at for better query performance
CREATE INDEX idx_product_deleted_at ON product(deleted_at);

-- +migrate Down
-- Remove soft delete support
DROP INDEX IF EXISTS idx_product_deleted_at;
ALTER TABLE product DROP COLUMN deleted_at;


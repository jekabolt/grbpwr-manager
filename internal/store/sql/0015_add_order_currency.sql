-- +migrate Up
-- Add currency column to customer_order table
-- Orders need to track which currency they were charged in for multi-currency support

ALTER TABLE customer_order 
ADD COLUMN currency VARCHAR(3) NOT NULL DEFAULT 'EUR' COMMENT 'ISO 4217 currency code (e.g., EUR, USD, JPY, CNY, KRW)';

-- Add index for currency queries
CREATE INDEX idx_order_currency ON customer_order(currency);

-- +migrate Down
-- Remove currency column and index
DROP INDEX idx_order_currency ON customer_order;
ALTER TABLE customer_order 
DROP COLUMN currency;


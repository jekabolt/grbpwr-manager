-- +migrate Up
-- Remove the base price column from product table
-- All prices are now stored in the product_price table with explicit currency codes
-- This ensures clarity and consistency in multi-currency pricing

-- Migrate existing price data to product_price table for all currencies
-- Creates 1:1 mapping for EUR, USD, JPY, CNY, KRW
INSERT INTO product_price (product_id, currency, price)
SELECT id, 'EUR', price FROM product WHERE price IS NOT NULL
UNION ALL
SELECT id, 'USD', price FROM product WHERE price IS NOT NULL
UNION ALL
SELECT id, 'JPY', price FROM product WHERE price IS NOT NULL
UNION ALL
SELECT id, 'CNY', price FROM product WHERE price IS NOT NULL
UNION ALL
SELECT id, 'KRW', price FROM product WHERE price IS NOT NULL
ON DUPLICATE KEY UPDATE price = VALUES(price);

ALTER TABLE product 
DROP COLUMN price;

-- +migrate Down
-- Restore the base price column
ALTER TABLE product 
ADD COLUMN price DECIMAL(10, 2) NULL CHECK (price >= 0);


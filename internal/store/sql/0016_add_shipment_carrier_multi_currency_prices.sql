-- +migrate Up
-- Create shipment_carrier_price table for multi-currency price support
-- This allows flexible pricing in different currencies for different locations
-- The shipment_carrier table's price column will be removed after migration

CREATE TABLE shipment_carrier_price (
    id INT PRIMARY KEY AUTO_INCREMENT,
    shipment_carrier_id INT NOT NULL,
    currency VARCHAR(3) NOT NULL COMMENT 'ISO 4217 currency code (e.g., EUR, USD, JPY, CNY, KRW, GBP)',
    price DECIMAL(10, 2) NOT NULL CHECK (price >= 0) COMMENT 'Price in the specified currency',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    FOREIGN KEY (shipment_carrier_id) REFERENCES shipment_carrier(id) ON DELETE CASCADE,
    UNIQUE KEY unique_carrier_currency (shipment_carrier_id, currency),
    INDEX idx_shipment_carrier_id (shipment_carrier_id),
    INDEX idx_currency (currency)
);

-- Migrate existing price data to shipment_carrier_price table for all supported currencies
-- Creates prices for EUR, USD, JPY, CNY, KRW, GBP for ALL shipment carriers
-- This ensures every carrier has prices in all 6 supported currencies (EUR, USD, JPY, CNY, KRW, GBP)
-- Each carrier will have 6 price records, one for each currency, all with the same initial price value
INSERT INTO shipment_carrier_price (shipment_carrier_id, currency, price)
SELECT id, 'EUR', price FROM shipment_carrier
UNION ALL
SELECT id, 'USD', price FROM shipment_carrier
UNION ALL
SELECT id, 'JPY', price FROM shipment_carrier
UNION ALL
SELECT id, 'CNY', price FROM shipment_carrier
UNION ALL
SELECT id, 'KRW', price FROM shipment_carrier
UNION ALL
SELECT id, 'GBP', price FROM shipment_carrier
ON DUPLICATE KEY UPDATE price = VALUES(price);

-- After migration, verify manually that all carriers have 6 prices:
-- SELECT sc.id, sc.carrier, COUNT(scp.currency) as currency_count
-- FROM shipment_carrier sc
-- LEFT JOIN shipment_carrier_price scp ON sc.id = scp.shipment_carrier_id
-- GROUP BY sc.id, sc.carrier
-- HAVING currency_count != 6 OR currency_count IS NULL;

-- Remove the base price column from shipment_carrier table
-- All prices are now stored in the shipment_carrier_price table with explicit currency codes
ALTER TABLE shipment_carrier 
DROP COLUMN price;

-- +migrate Down
-- Restore the base price column
ALTER TABLE shipment_carrier 
ADD COLUMN price DECIMAL(10, 2) NOT NULL DEFAULT 0;

-- Migrate EUR prices back to the base price column
UPDATE shipment_carrier sc
INNER JOIN shipment_carrier_price scp ON sc.id = scp.shipment_carrier_id
SET sc.price = scp.price
WHERE scp.currency = 'EUR';

-- Remove shipment_carrier_price table
DROP TABLE IF EXISTS shipment_carrier_price;


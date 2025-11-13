-- +migrate Up
-- Create product_price table for multi-currency price support
-- This allows flexible pricing in different currencies for different locations
-- The product table's price column will serve as the base/fallback price

CREATE TABLE product_price (
    id INT PRIMARY KEY AUTO_INCREMENT,
    product_id INT NOT NULL,
    currency VARCHAR(3) NOT NULL COMMENT 'ISO 4217 currency code (e.g., EUR, USD, JPY, CNY, KRW)',
    price DECIMAL(10, 2) NOT NULL CHECK (price >= 0) COMMENT 'Price in the specified currency',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    FOREIGN KEY (product_id) REFERENCES product(id) ON DELETE CASCADE,
    UNIQUE KEY unique_product_currency (product_id, currency),
    INDEX idx_product_id (product_id),
    INDEX idx_currency (currency)
) COMMENT 'Stores product prices in different currencies';

-- +migrate Down
-- Remove product_price table
DROP TABLE IF EXISTS product_price;


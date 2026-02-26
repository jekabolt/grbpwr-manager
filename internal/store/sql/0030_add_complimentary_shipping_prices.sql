-- +migrate Up
-- Migration: Add complimentary shipping prices table
-- Purpose: Store complimentary (free) shipping threshold prices in multiple currencies
-- Affected: New table complimentary_shipping_price
-- Date: 2026-02-26

-- Create table to store complimentary shipping price thresholds per currency
-- When order total exceeds this threshold, shipping becomes complimentary
CREATE TABLE complimentary_shipping_price (
    id INT AUTO_INCREMENT PRIMARY KEY,
    currency VARCHAR(3) NOT NULL UNIQUE COMMENT 'ISO 4217 currency code (e.g., USD, EUR, JPY, CNY, KRW)',
    price DECIMAL(10, 2) NOT NULL COMMENT 'Minimum order total to qualify for complimentary shipping',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_currency (currency)
) COMMENT 'Stores complimentary shipping threshold prices in multiple currencies';

-- Insert default complimentary shipping thresholds for supported currencies
INSERT INTO complimentary_shipping_price (currency, price)
VALUES
    ('USD', 100.00),
    ('EUR', 100.00),
    ('JPY', 15000),
    ('CNY', 700),
    ('KRW', 130000);

-- +migrate Down
DROP TABLE IF EXISTS complimentary_shipping_price;

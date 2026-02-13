-- +migrate Up
-- Migration: Add extended fields for shipment carriers
-- Purpose: expected_delivery_time, geo restrictions by region, seed DPD and FedEx
-- Affected tables: shipment_carrier, shipment_carrier_region (new)

-- Add expected_delivery_time column
ALTER TABLE shipment_carrier
ADD COLUMN expected_delivery_time VARCHAR(100) NULL COMMENT 'e.g. 3-5 business days';

-- Create shipment_carrier_region table for geo restrictions
-- Regions: AFRICA, AMERICAS, ASIA PACIFIC, EUROPE, MIDDLE EAST
CREATE TABLE shipment_carrier_region (
    id INT PRIMARY KEY AUTO_INCREMENT,
    shipment_carrier_id INT NOT NULL,
    region VARCHAR(50) NOT NULL COMMENT 'AFRICA, AMERICAS, ASIA PACIFIC, EUROPE, MIDDLE EAST',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    FOREIGN KEY (shipment_carrier_id) REFERENCES shipment_carrier(id) ON DELETE CASCADE,
    UNIQUE KEY unique_carrier_region (shipment_carrier_id, region),
    INDEX idx_shipment_carrier_id (shipment_carrier_id)
) COMMENT 'Regions where carrier is available; empty = global';

-- Seed DPD and FedEx (DHL and FREE already exist from 0001)
INSERT INTO shipment_carrier (carrier, tracking_url, allowed, description, expected_delivery_time)
VALUES
    ('DPD', 'https://tracking.dpd.de/status/en_US/parcel/%s', TRUE, 'DPD parcel tracking with international delivery.', '3-5 business days'),
    ('FedEx', 'https://www.fedex.com/fedextrack/?trknbr=%s', TRUE, 'FedEx international shipping with express options.', '2-4 business days')
ON DUPLICATE KEY UPDATE carrier = carrier;

-- Add prices for DPD and FedEx in all 6 currencies (only for newly inserted rows)
-- Use subquery to get IDs of DPD and FedEx, insert prices
INSERT INTO shipment_carrier_price (shipment_carrier_id, currency, price)
SELECT id, 'EUR', 10.99 FROM shipment_carrier WHERE carrier = 'DPD'
UNION ALL SELECT id, 'USD', 10.99 FROM shipment_carrier WHERE carrier = 'DPD'
UNION ALL SELECT id, 'GBP', 10.99 FROM shipment_carrier WHERE carrier = 'DPD'
UNION ALL SELECT id, 'JPY', 10.99 FROM shipment_carrier WHERE carrier = 'DPD'
UNION ALL SELECT id, 'CNY', 10.99 FROM shipment_carrier WHERE carrier = 'DPD'
UNION ALL SELECT id, 'KRW', 10.99 FROM shipment_carrier WHERE carrier = 'DPD'
UNION ALL SELECT id, 'EUR', 12.99 FROM shipment_carrier WHERE carrier = 'FedEx'
UNION ALL SELECT id, 'USD', 12.99 FROM shipment_carrier WHERE carrier = 'FedEx'
UNION ALL SELECT id, 'GBP', 12.99 FROM shipment_carrier WHERE carrier = 'FedEx'
UNION ALL SELECT id, 'JPY', 12.99 FROM shipment_carrier WHERE carrier = 'FedEx'
UNION ALL SELECT id, 'CNY', 12.99 FROM shipment_carrier WHERE carrier = 'FedEx'
UNION ALL SELECT id, 'KRW', 12.99 FROM shipment_carrier WHERE carrier = 'FedEx'
ON DUPLICATE KEY UPDATE price = VALUES(price);

-- +migrate Down
DROP TABLE IF EXISTS shipment_carrier_region;
ALTER TABLE shipment_carrier DROP COLUMN expected_delivery_time;

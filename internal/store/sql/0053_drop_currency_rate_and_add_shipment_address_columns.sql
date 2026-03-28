-- +migrate Up
-- Description: Remove unused currency_rate table; add shipment.delivered_at; add address.country_code (ISO 3166-1 alpha-2) with backfill

DROP TABLE IF EXISTS currency_rate;

ALTER TABLE shipment
  ADD COLUMN delivered_at DATETIME NULL DEFAULT NULL
  COMMENT 'Actual delivery confirmation timestamp'
  AFTER estimated_arrival_date;

ALTER TABLE address
  ADD COLUMN country_code CHAR(2) NULL DEFAULT NULL
  COMMENT 'ISO 3166-1 alpha-2 code derived from country string'
  AFTER country;

CREATE INDEX idx_address_country_code ON address (country_code);

UPDATE address SET country_code = UPPER(country) WHERE LENGTH(country) = 2 AND country != 'string';

UPDATE address SET country_code = NULL WHERE country = 'string';

-- +migrate Down
DROP INDEX idx_address_country_code ON address;
ALTER TABLE address DROP COLUMN country_code;

ALTER TABLE shipment DROP COLUMN delivered_at;

CREATE TABLE currency_rate (
    id INT PRIMARY KEY AUTO_INCREMENT,
    currency_code VARCHAR(255) NOT NULL UNIQUE,
    rate DECIMAL(10, 2) NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL
);

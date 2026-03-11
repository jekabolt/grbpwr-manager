-- +migrate Up
-- Widen the sku column to accommodate the new season-year suffix (e.g. -SS26)
ALTER TABLE product MODIFY COLUMN sku VARCHAR(30) NOT NULL DEFAULT '';

-- Update existing product SKUs to append the season-year segment.
-- Format: existing_sku + '-' + season + last_two_digits_of_created_at_year
-- e.g. OTH-BLA-U1771975603 → OTH-BLA-U1771975603-SS26
UPDATE product
SET sku = CONCAT(sku, '-', season, LPAD(YEAR(created_at) % 100, 2, '0'))
WHERE sku NOT REGEXP '-[A-Z]{2}[0-9]{2}$';

-- +migrate Down
-- Strip the trailing season-year segment from SKUs (last 5 chars: '-XX00')
UPDATE product
SET sku = LEFT(sku, LENGTH(sku) - 5)
WHERE sku REGEXP '-[A-Z]{2}[0-9]{2}$';

-- Restore original column width
ALTER TABLE product MODIFY COLUMN sku VARCHAR(20) NOT NULL DEFAULT '';
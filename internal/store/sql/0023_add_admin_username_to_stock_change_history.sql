-- +migrate Up
ALTER TABLE product_stock_change_history
ADD COLUMN admin_username VARCHAR(255) NULL COMMENT 'Admin username who made the change (for admin flows only)';

-- +migrate Down
ALTER TABLE product_stock_change_history
DROP COLUMN admin_username;

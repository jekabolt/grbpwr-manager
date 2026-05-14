-- +migrate Up
-- Description: Add contact phone to storefront saved shipping addresses

ALTER TABLE storefront_saved_address
  ADD COLUMN phone VARCHAR(32) NULL DEFAULT NULL COMMENT 'Contact phone for this address' AFTER postal_code;

-- +migrate Down

ALTER TABLE storefront_saved_address
  DROP COLUMN phone;

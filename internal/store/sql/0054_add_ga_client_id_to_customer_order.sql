-- +migrate Up
-- Description: Add ga_client_id column to customer_order for GA4 Measurement Protocol attribution
-- Affected tables: customer_order
-- Type: additive (non-breaking)

ALTER TABLE customer_order
  ADD COLUMN ga_client_id VARCHAR(150) NULL DEFAULT NULL COMMENT 'GA4 client ID from browser _ga cookie for server-side purchase attribution';

-- +migrate Down

ALTER TABLE customer_order
  DROP COLUMN ga_client_id;

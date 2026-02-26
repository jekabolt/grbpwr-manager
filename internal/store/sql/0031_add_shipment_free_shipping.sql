-- +migrate Up
-- Migration: Add free_shipping column to shipment table
-- Purpose: Track whether shipping was complimentary (threshold-based or promo-based)
-- Affected: shipment table
-- Date: 2026-02-26

ALTER TABLE shipment
ADD COLUMN free_shipping TINYINT(1) NOT NULL DEFAULT 0 COMMENT 'True when shipping was waived (complimentary threshold or promo)';

-- +migrate Down
ALTER TABLE shipment DROP COLUMN free_shipping;

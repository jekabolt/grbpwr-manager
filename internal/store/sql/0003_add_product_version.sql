-- +migrate Up
-- Migration: Add version field to product table
-- Purpose: Add a version field to track product versions
-- Affected tables: product

-- Add version column to product table
-- This field will store version information for products as a required string field
ALTER TABLE product 
ADD COLUMN version VARCHAR(255) NOT NULL DEFAULT '';

-- Update the default value constraint after adding the column
-- This ensures existing products get an empty string as default
-- Future inserts will require an explicit version value
ALTER TABLE product 
ALTER COLUMN version DROP DEFAULT;

-- +migrate Down
-- Remove version column from product table
ALTER TABLE product 
DROP COLUMN version;

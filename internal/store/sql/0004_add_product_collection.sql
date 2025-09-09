-- +migrate Up
-- Migration: Add collection field and auto-generated SKU column
-- Purpose: Add collection field and modify SKU to be auto-generated based on product attributes
-- Affected tables: product

-- Add collection column to product table
-- This field will store collection information for products
ALTER TABLE product 
ADD COLUMN collection VARCHAR(255) NOT NULL DEFAULT '';

-- Drop existing SKU column and recreate as generated column
ALTER TABLE product DROP COLUMN sku;

-- Add auto-generated SKU column
-- SKU Format: COLLECTION(4)-TOP_CATEGORY_ID_SUB_CATEGORY_ID_TYPE_ID-COLOR(3)-VERSION
-- Example: SYNC-2_15_1-NAV-2026411
ALTER TABLE product ADD COLUMN sku VARCHAR(50) GENERATED ALWAYS AS (
    CONCAT(
        -- Collection: first 4 characters, uppercase
        UPPER(LEFT(COALESCE(collection, 'UNKN'), 4)),
        '-',
        -- Category: numbers separated by underscores  
        COALESCE(top_category_id, 0),
        '_',
        COALESCE(sub_category_id, 0), 
        '_',
        COALESCE(type_id, 0),
        '-',
        -- Color: first 3 characters, uppercase
        UPPER(LEFT(COALESCE(color, 'UNK'), 3)),
        '-',
        -- Version: remove dots and dashes
        REPLACE(REPLACE(COALESCE(version, '0'), '.', ''), '-', '')
    )
) STORED;

-- Add unique constraint on the generated SKU
ALTER TABLE product ADD UNIQUE INDEX sku (sku);

-- +migrate Down
-- Remove generated SKU column and recreate original SKU column
ALTER TABLE product DROP COLUMN sku;
ALTER TABLE product ADD COLUMN sku VARCHAR(255) NOT NULL UNIQUE;

-- Remove collection column from product table
ALTER TABLE product 
DROP COLUMN collection;

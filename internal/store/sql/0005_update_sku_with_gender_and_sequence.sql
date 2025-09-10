-- +migrate Up
-- Migration: Update SKU generation to include gender and sequence number
-- Purpose: Modify SKU to include gender (M/F/U) and handle duplicate SKUs with incremental sequence numbers
-- Affected tables: product
-- SKU Format: COLLECTION(4)-TOP_CATEGORY_ID_SUB_CATEGORY_ID_TYPE_ID-COLOR(3)-VERSION-GENDER-SEQUENCE
-- Example: SYNC-2_15_1-NAV-2026411-M-3400

-- First, drop the existing generated SKU column and unique constraint
ALTER TABLE product DROP INDEX sku;
ALTER TABLE product DROP COLUMN sku;

-- Add the new SKU column as a generated column with gender and sequence logic
ALTER TABLE product ADD COLUMN sku VARCHAR(70) GENERATED ALWAYS AS (
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
        -- Version: remove dots, dashes, and all letters (keep only numbers)
        REGEXP_REPLACE(COALESCE(version, '0'), '[^0-9]', ''),
        '-',
        -- Gender: map target_gender to single letter
        CASE 
            WHEN target_gender = 'male' THEN 'M'
            WHEN target_gender = 'female' THEN 'F' 
            WHEN target_gender = 'unisex' THEN 'U'
            ELSE 'U'
        END,
        '-',
        -- Sequence: use last 4 digits of CRC32 hash of created_at for deterministic "randomness"
        RIGHT(LPAD(CRC32(created_at), 10, '0'), 4)
    )
) STORED;

-- Add unique constraint on the new SKU column
ALTER TABLE product ADD UNIQUE INDEX sku (sku);

-- +migrate Down
-- Remove the new SKU column and recreate the old generated column
ALTER TABLE product DROP INDEX sku;
ALTER TABLE product DROP COLUMN sku;

-- Recreate the old auto-generated SKU column
-- SKU Format: COLLECTION(4)-TOP_CATEGORY_ID_SUB_CATEGORY_ID_TYPE_ID-COLOR(3)-VERSION
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
        -- Version: remove dots, dashes, and all letters (keep only numbers)
        REGEXP_REPLACE(COALESCE(version, '0'), '[^0-9]', '')
    )
) STORED;

-- Add unique constraint on the generated SKU
ALTER TABLE product ADD UNIQUE INDEX sku (sku);

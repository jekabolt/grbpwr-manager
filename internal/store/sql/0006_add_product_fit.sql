-- +migrate Up
-- Migration: Add fit field to product table
-- Purpose: Add fit column to store product fit information (e.g., regular, loose, relaxed, skinny, cropped, tailored, etc.)
-- Affected tables: product

ALTER TABLE product ADD COLUMN fit VARCHAR(50) NULL;

-- Fix category translations
-- French: outerwear
UPDATE category_translation SET name = 'vestes et manteaux' WHERE id = 353;

-- English: slippers_loafers to slippers
UPDATE category_translation SET name = 'slippers' WHERE id IN (126, 127);

-- French: loungewear
UPDATE category_translation SET name = 'loungewear' WHERE id = 336;

-- French: mocassins to chaussons (slippers)
UPDATE category_translation SET name = 'chaussons' WHERE id IN (381, 382);

-- French: swimwear (combine men/women)
UPDATE category_translation SET name = 'maillots de bain' WHERE id IN (391, 392);

-- Italian: mocassini to pantofole (slippers)
UPDATE category_translation SET name = 'pantofole' WHERE id IN (891, 892);

-- Japanese: slippers
UPDATE category_translation SET name = 'スリッパ' WHERE id IN (1146, 1147);

-- Chinese: slippers
UPDATE category_translation SET name = '拖鞋' WHERE id IN (1401, 1402);

-- Korean: slippers
UPDATE category_translation SET name = '슬리퍼' WHERE id IN (1656, 1657);

-- German: loungewear
UPDATE category_translation SET name = 'loungewear' WHERE id = 591;

-- Italian: loungewear
UPDATE category_translation SET name = 'loungewear' WHERE id = 846;

-- Japanese: loungewear
UPDATE category_translation SET name = 'ラウンジウェア' WHERE id = 1101;

-- Chinese: loungewear
UPDATE category_translation SET name = '家居服' WHERE id = 1356;

-- Korean: loungewear
UPDATE category_translation SET name = '라운지웨어' WHERE id = 1611;

-- Japanese: outerwear
UPDATE category_translation SET name = 'アウターウェア' WHERE id = 1118;

-- Replace spaces with underscores in all Latin-based language translations (en, fr, de, it)
-- This ensures consistency with the naming convention used throughout the system
UPDATE category_translation 
SET name = REPLACE(name, ' ', '_') 
WHERE language_id IN (
    SELECT id FROM language WHERE code IN ('en', 'fr', 'de', 'it')
);

-- Update view_categories to include all translations
DROP VIEW IF EXISTS view_categories;

CREATE VIEW view_categories AS
SELECT 
    c1.id AS category_id,
    c1.level_id,
    cl.name AS level_name,
    c1.parent_id,
    c1.created_at,
    c1.updated_at,
    ct_en.name AS name_en,
    ct_fr.name AS name_fr,
    ct_de.name AS name_de,
    ct_it.name AS name_it,
    ct_ja.name AS name_ja,
    ct_cn.name AS name_cn,
    ct_kr.name AS name_kr,
    ct_parent_en.name AS parent_name_en,
    ct_parent_fr.name AS parent_name_fr,
    ct_parent_de.name AS parent_name_de,
    ct_parent_it.name AS parent_name_it,
    ct_parent_ja.name AS parent_name_ja,
    ct_parent_cn.name AS parent_name_cn,
    ct_parent_kr.name AS parent_name_kr
FROM 
    category c1
LEFT JOIN 
    category c2 ON c1.parent_id = c2.id
JOIN 
    category_level cl ON c1.level_id = cl.id
LEFT JOIN 
    category_translation ct_en ON c1.id = ct_en.category_id 
    AND ct_en.language_id = (SELECT id FROM language WHERE code = 'en')
LEFT JOIN 
    category_translation ct_fr ON c1.id = ct_fr.category_id 
    AND ct_fr.language_id = (SELECT id FROM language WHERE code = 'fr')
LEFT JOIN 
    category_translation ct_de ON c1.id = ct_de.category_id 
    AND ct_de.language_id = (SELECT id FROM language WHERE code = 'de')
LEFT JOIN 
    category_translation ct_it ON c1.id = ct_it.category_id 
    AND ct_it.language_id = (SELECT id FROM language WHERE code = 'it')
LEFT JOIN 
    category_translation ct_ja ON c1.id = ct_ja.category_id 
    AND ct_ja.language_id = (SELECT id FROM language WHERE code = 'ja')
LEFT JOIN 
    category_translation ct_cn ON c1.id = ct_cn.category_id 
    AND ct_cn.language_id = (SELECT id FROM language WHERE code = 'cn')
LEFT JOIN 
    category_translation ct_kr ON c1.id = ct_kr.category_id 
    AND ct_kr.language_id = (SELECT id FROM language WHERE code = 'kr')
LEFT JOIN 
    category_translation ct_parent_en ON c2.id = ct_parent_en.category_id 
    AND ct_parent_en.language_id = (SELECT id FROM language WHERE code = 'en')
LEFT JOIN 
    category_translation ct_parent_fr ON c2.id = ct_parent_fr.category_id 
    AND ct_parent_fr.language_id = (SELECT id FROM language WHERE code = 'fr')
LEFT JOIN 
    category_translation ct_parent_de ON c2.id = ct_parent_de.category_id 
    AND ct_parent_de.language_id = (SELECT id FROM language WHERE code = 'de')
LEFT JOIN 
    category_translation ct_parent_it ON c2.id = ct_parent_it.category_id 
    AND ct_parent_it.language_id = (SELECT id FROM language WHERE code = 'it')
LEFT JOIN 
    category_translation ct_parent_ja ON c2.id = ct_parent_ja.category_id 
    AND ct_parent_ja.language_id = (SELECT id FROM language WHERE code = 'ja')
LEFT JOIN 
    category_translation ct_parent_cn ON c2.id = ct_parent_cn.category_id 
    AND ct_parent_cn.language_id = (SELECT id FROM language WHERE code = 'cn')
LEFT JOIN 
    category_translation ct_parent_kr ON c2.id = ct_parent_kr.category_id 
    AND ct_parent_kr.language_id = (SELECT id FROM language WHERE code = 'kr');

-- +migrate Down
-- Remove the fit column
ALTER TABLE product DROP COLUMN fit;

-- Restore original view_categories
DROP VIEW IF EXISTS view_categories;

CREATE VIEW view_categories AS
SELECT 
    c1.id AS category_id,
    c1.name AS category_name,
    c1.level_id,
    cl.name AS level_name,
    c1.parent_id,
    c2.name AS parent_name,
    c1.created_at,
    c1.updated_at
FROM 
    category c1
LEFT JOIN 
    category c2 ON c1.parent_id = c2.id
JOIN 
    category_level cl ON c1.level_id = cl.id;

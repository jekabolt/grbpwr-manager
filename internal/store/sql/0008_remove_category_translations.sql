-- +migrate Up
-- Remove category and measurement name translations and return to English-only names
-- This migration simplifies the structure by removing multilingual support

-- Add name column back to category table
ALTER TABLE category
ADD COLUMN name VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'English category name';

-- Populate name column with English translations from category_translation
UPDATE category c
JOIN category_translation ct ON c.id = ct.category_id
JOIN language l ON ct.language_id = l.id
SET c.name = ct.name
WHERE l.code = 'en';

-- Add name column back to measurement_name table
ALTER TABLE measurement_name
ADD COLUMN name VARCHAR(50) NOT NULL DEFAULT '' COMMENT 'English measurement name';

-- Populate name column with English translations from measurement_name_translation
UPDATE measurement_name mn
JOIN measurement_name_translation mnt ON mn.id = mnt.measurement_name_id
JOIN language l ON mnt.language_id = l.id
SET mn.name = mnt.name
WHERE l.code = 'en';

-- Drop the translation tables
-- This will automatically drop all indexes and foreign key constraints
DROP TABLE IF EXISTS category_translation;
DROP TABLE IF EXISTS measurement_name_translation;

-- Update view_categories to use the name column directly
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

-- Re-add unique constraint for category name and parent
ALTER TABLE category 
ADD CONSTRAINT unique_name_parent UNIQUE (name, parent_id);

-- Add unique constraint for measurement name
ALTER TABLE measurement_name
ADD CONSTRAINT unique_measurement_name UNIQUE (name);

-- +migrate Down
-- Restore category and measurement name translations support
-- WARNING: This will lose non-English translations

-- Drop the unique constraints
ALTER TABLE category DROP INDEX unique_name_parent;
ALTER TABLE measurement_name DROP INDEX unique_measurement_name;

-- Create category_translation table
CREATE TABLE category_translation (
    id INT PRIMARY KEY AUTO_INCREMENT,
    category_id INT NOT NULL,
    language_id INT NOT NULL,
    name VARCHAR(255) NOT NULL COMMENT 'Translated category name',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    UNIQUE KEY unique_category_language (category_id, language_id),
    FOREIGN KEY (category_id) REFERENCES category(id) ON DELETE CASCADE,
    FOREIGN KEY (language_id) REFERENCES language(id) ON DELETE CASCADE
) COMMENT 'Category translations for different languages';

-- Create measurement_name_translation table
CREATE TABLE measurement_name_translation (
    id INT PRIMARY KEY AUTO_INCREMENT,
    measurement_name_id INT NOT NULL,
    language_id INT NOT NULL,
    name VARCHAR(50) NOT NULL COMMENT 'Translated measurement name',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    UNIQUE KEY unique_measurement_language (measurement_name_id, language_id),
    FOREIGN KEY (measurement_name_id) REFERENCES measurement_name(id) ON DELETE CASCADE,
    FOREIGN KEY (language_id) REFERENCES language(id) ON DELETE CASCADE
) COMMENT 'Measurement name translations for different languages';

-- Create indexes
CREATE INDEX idx_category_translation_category_id ON category_translation(category_id);
CREATE INDEX idx_category_translation_language_id ON category_translation(language_id);
CREATE INDEX idx_measurement_name_translation_measurement_id ON measurement_name_translation(measurement_name_id);
CREATE INDEX idx_measurement_name_translation_language_id ON measurement_name_translation(language_id);

-- Populate category_translation with English translations from category table
INSERT INTO category_translation (category_id, language_id, name)
SELECT 
    c.id,
    l.id,
    c.name
FROM category c
CROSS JOIN language l
WHERE l.code = 'en';

-- Populate measurement_name_translation with English translations from measurement_name table
INSERT INTO measurement_name_translation (measurement_name_id, language_id, name)
SELECT 
    mn.id,
    l.id,
    mn.name
FROM measurement_name mn
CROSS JOIN language l
WHERE l.code = 'en';

-- Drop the name columns
ALTER TABLE category DROP COLUMN name;
ALTER TABLE measurement_name DROP COLUMN name;

-- Restore the old view_categories
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
    ct_parent_en.name AS parent_name_en
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
    category_translation ct_parent_en ON c2.id = ct_parent_en.category_id 
    AND ct_parent_en.language_id = (SELECT id FROM language WHERE code = 'en');


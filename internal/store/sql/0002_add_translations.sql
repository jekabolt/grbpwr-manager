-- +migrate Up
-- Translation support for products, archive, hero, categories, and measurement names
-- This migration adds internationalization support and normalizes translatable fields
-- IMPORTANT: This migration moves translatable fields from main tables to translation tables

-- Create language table to define supported languages
CREATE TABLE language (
    id INT PRIMARY KEY AUTO_INCREMENT,
    code VARCHAR(5) NOT NULL UNIQUE COMMENT 'ISO 639-1 language code (e.g., en, es, fr)',
    name VARCHAR(100) NOT NULL COMMENT 'Human readable language name',
    is_default BOOLEAN DEFAULT FALSE COMMENT 'Indicates the default/fallback language',
    is_active BOOLEAN DEFAULT TRUE COMMENT 'Whether this language is currently supported',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL
) COMMENT 'Supported languages for translations';

-- Insert default languages
INSERT INTO language (code, name, is_default, is_active) VALUES 
    ('en', 'English', true, true),
    ('fr', 'French', false, true),
    ('de', 'German', false, true),
    ('it', 'Italian', false, true),
    ('ja', 'Japanese', false, true),
    ('cn', 'Chinese', false, true),
    ('kr', 'Korean', false, true);

-- Create translation tables for all translatable entities

-- Product translations table
CREATE TABLE product_translation (
    id INT PRIMARY KEY AUTO_INCREMENT,
    product_id INT NOT NULL,
    language_id INT NOT NULL,
    name VARCHAR(255) NOT NULL COMMENT 'Translated product name',
    description TEXT NOT NULL COMMENT 'Translated product description',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    UNIQUE KEY unique_product_language (product_id, language_id),
    FOREIGN KEY (product_id) REFERENCES product(id) ON DELETE CASCADE,
    FOREIGN KEY (language_id) REFERENCES language(id) ON DELETE CASCADE
) COMMENT 'Product translations for different languages';

-- Archive translations table
CREATE TABLE archive_translation (
    id INT PRIMARY KEY AUTO_INCREMENT,
    archive_id INT NOT NULL,
    language_id INT NOT NULL,
    heading VARCHAR(255) NOT NULL COMMENT 'Translated archive heading',
    description TEXT NOT NULL COMMENT 'Translated archive description',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    UNIQUE KEY unique_archive_language (archive_id, language_id),
    FOREIGN KEY (archive_id) REFERENCES archive(id) ON DELETE CASCADE,
    FOREIGN KEY (language_id) REFERENCES language(id) ON DELETE CASCADE
) COMMENT 'Archive translations for different languages';

-- Category translations table
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

-- Measurement name translations table
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

-- Hero single translations table
CREATE TABLE hero_single_translation (
    id INT PRIMARY KEY AUTO_INCREMENT,
    hero_single_id INT NOT NULL COMMENT 'References the hero single entity in hero table JSON data',
    language_id INT NOT NULL,
    headline VARCHAR(255) NOT NULL COMMENT 'Translated hero headline',
    explore_text VARCHAR(255) NOT NULL COMMENT 'Translated explore text',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    UNIQUE KEY unique_hero_single_language (hero_single_id, language_id),
    FOREIGN KEY (language_id) REFERENCES language(id) ON DELETE CASCADE
) COMMENT 'Hero single translations for different languages';

-- Hero featured products translations table
CREATE TABLE hero_featured_products_translation (
    id INT PRIMARY KEY AUTO_INCREMENT,
    hero_featured_products_id INT NOT NULL COMMENT 'References the hero featured products entity in hero table JSON data',
    language_id INT NOT NULL,
    headline VARCHAR(255) NOT NULL COMMENT 'Translated featured products headline',
    explore_text VARCHAR(255) NOT NULL COMMENT 'Translated explore text',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    UNIQUE KEY unique_hero_featured_products_language (hero_featured_products_id, language_id),
    FOREIGN KEY (language_id) REFERENCES language(id) ON DELETE CASCADE
) COMMENT 'Hero featured products translations for different languages';

-- Hero featured products tag translations table
CREATE TABLE hero_featured_products_tag_translation (
    id INT PRIMARY KEY AUTO_INCREMENT,
    hero_featured_products_tag_id INT NOT NULL COMMENT 'References the hero featured products tag entity in hero table JSON data',
    language_id INT NOT NULL,
    headline VARCHAR(255) NOT NULL COMMENT 'Translated featured products tag headline',
    explore_text VARCHAR(255) NOT NULL COMMENT 'Translated explore text',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    UNIQUE KEY unique_hero_featured_products_tag_language (hero_featured_products_tag_id, language_id),
    FOREIGN KEY (language_id) REFERENCES language(id) ON DELETE CASCADE
) COMMENT 'Hero featured products tag translations for different languages';

-- Hero featured archive translations table
CREATE TABLE hero_featured_archive_translation (
    id INT PRIMARY KEY AUTO_INCREMENT,
    hero_featured_archive_id INT NOT NULL COMMENT 'References the hero featured archive entity in hero table JSON data',
    language_id INT NOT NULL,
    headline VARCHAR(255) NOT NULL COMMENT 'Translated featured archive headline',
    explore_text VARCHAR(255) NOT NULL COMMENT 'Translated explore text',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    UNIQUE KEY unique_hero_featured_archive_language (hero_featured_archive_id, language_id),
    FOREIGN KEY (language_id) REFERENCES language(id) ON DELETE CASCADE
) COMMENT 'Hero featured archive translations for different languages';

-- Create indexes for optimized translation queries
CREATE INDEX idx_product_translation_product_id ON product_translation(product_id);
CREATE INDEX idx_product_translation_language_id ON product_translation(language_id);

CREATE INDEX idx_archive_translation_archive_id ON archive_translation(archive_id);
CREATE INDEX idx_archive_translation_language_id ON archive_translation(language_id);

CREATE INDEX idx_category_translation_category_id ON category_translation(category_id);
CREATE INDEX idx_category_translation_language_id ON category_translation(language_id);

CREATE INDEX idx_measurement_name_translation_measurement_id ON measurement_name_translation(measurement_name_id);
CREATE INDEX idx_measurement_name_translation_language_id ON measurement_name_translation(language_id);

CREATE INDEX idx_hero_single_translation_hero_id ON hero_single_translation(hero_single_id);
CREATE INDEX idx_hero_single_translation_language_id ON hero_single_translation(language_id);

CREATE INDEX idx_hero_featured_products_translation_hero_id ON hero_featured_products_translation(hero_featured_products_id);
CREATE INDEX idx_hero_featured_products_translation_language_id ON hero_featured_products_translation(language_id);

CREATE INDEX idx_hero_featured_products_tag_translation_hero_id ON hero_featured_products_tag_translation(hero_featured_products_tag_id);
CREATE INDEX idx_hero_featured_products_tag_translation_language_id ON hero_featured_products_tag_translation(language_id);

CREATE INDEX idx_hero_featured_archive_translation_hero_id ON hero_featured_archive_translation(hero_featured_archive_id);
CREATE INDEX idx_hero_featured_archive_translation_language_id ON hero_featured_archive_translation(language_id);

-- Migrate existing data to translation tables for the default language (English)
-- This preserves existing content while enabling multilingual support

-- Get the default language ID (English)
SET @default_language_id = (SELECT id FROM language WHERE is_default = TRUE LIMIT 1);

-- Migrate product data to product_translation table
INSERT INTO product_translation (product_id, language_id, name, description)
SELECT 
    id,
    @default_language_id,
    name,
    description
FROM product;

-- Migrate archive data to archive_translation table
INSERT INTO archive_translation (archive_id, language_id, heading, description)
SELECT 
    id,
    @default_language_id,
    heading,
    description
FROM archive;

-- Migrate category data to category_translation table
INSERT INTO category_translation (category_id, language_id, name)
SELECT 
    id,
    @default_language_id,
    name
FROM category;

-- Migrate measurement name data to measurement_name_translation table
INSERT INTO measurement_name_translation (measurement_name_id, language_id, name)
SELECT 
    id,
    @default_language_id,
    name
FROM measurement_name;

-- Insert comprehensive translations for all measurement names in all supported languages
-- English translations (already inserted above as default language)

-- French translations
INSERT INTO measurement_name_translation (measurement_name_id, language_id, name)
SELECT 
    mn.id,
    l.id,
    CASE mn.name
        WHEN 'waist' THEN 'taille'
        WHEN 'inseam' THEN 'entrejambe'
        WHEN 'length' THEN 'longueur'
        WHEN 'rise' THEN 'hauteur de taille'
        WHEN 'hips' THEN 'hanches'
        WHEN 'shoulders' THEN 'épaules'
        WHEN 'chest' THEN 'poitrine'
        WHEN 'sleeve' THEN 'manche'
        WHEN 'width' THEN 'largeur'
        WHEN 'leg-opening' THEN 'ouverture de jambe'
        WHEN 'hip' THEN 'hanche'
        WHEN 'bottom-width' THEN 'largeur du bas'
        WHEN 'depth' THEN 'profondeur'
        WHEN 'start-fit-length' THEN 'longueur début ajusté'
        WHEN 'end-fit-length' THEN 'longueur fin ajusté'
        WHEN 'height' THEN 'hauteur'
    END
FROM measurement_name mn
CROSS JOIN language l
WHERE l.code = 'fr';

-- German translations
INSERT INTO measurement_name_translation (measurement_name_id, language_id, name)
SELECT 
    mn.id,
    l.id,
    CASE mn.name
        WHEN 'waist' THEN 'taille'
        WHEN 'inseam' THEN 'innenbeinlänge'
        WHEN 'length' THEN 'länge'
        WHEN 'rise' THEN 'leibhöhe'
        WHEN 'hips' THEN 'hüften'
        WHEN 'shoulders' THEN 'schultern'
        WHEN 'chest' THEN 'brust'
        WHEN 'sleeve' THEN 'ärmel'
        WHEN 'width' THEN 'breite'
        WHEN 'leg-opening' THEN 'beinöffnung'
        WHEN 'hip' THEN 'hüfte'
        WHEN 'bottom-width' THEN 'untere breite'
        WHEN 'depth' THEN 'tiefe'
        WHEN 'start-fit-length' THEN 'anfang passform länge'
        WHEN 'end-fit-length' THEN 'ende passform länge'
        WHEN 'height' THEN 'höhe'
    END
FROM measurement_name mn
CROSS JOIN language l
WHERE l.code = 'de';

-- Italian translations
INSERT INTO measurement_name_translation (measurement_name_id, language_id, name)
SELECT 
    mn.id,
    l.id,
    CASE mn.name
        WHEN 'waist' THEN 'vita'
        WHEN 'inseam' THEN 'cavallo'
        WHEN 'length' THEN 'lunghezza'
        WHEN 'rise' THEN 'altezza vita'
        WHEN 'hips' THEN 'fianchi'
        WHEN 'shoulders' THEN 'spalle'
        WHEN 'chest' THEN 'petto'
        WHEN 'sleeve' THEN 'manica'
        WHEN 'width' THEN 'larghezza'
        WHEN 'leg-opening' THEN 'apertura gamba'
        WHEN 'hip' THEN 'fianco'
        WHEN 'bottom-width' THEN 'larghezza inferiore'
        WHEN 'depth' THEN 'profondità'
        WHEN 'start-fit-length' THEN 'lunghezza inizio vestibilità'
        WHEN 'end-fit-length' THEN 'lunghezza fine vestibilità'
        WHEN 'height' THEN 'altezza'
    END
FROM measurement_name mn
CROSS JOIN language l
WHERE l.code = 'it';

-- Japanese translations
INSERT INTO measurement_name_translation (measurement_name_id, language_id, name)
SELECT 
    mn.id,
    l.id,
    CASE mn.name
        WHEN 'waist' THEN 'ウエスト'
        WHEN 'inseam' THEN '股下'
        WHEN 'length' THEN '丈'
        WHEN 'rise' THEN 'ライズ'
        WHEN 'hips' THEN 'ヒップ'
        WHEN 'shoulders' THEN '肩幅'
        WHEN 'chest' THEN '胸囲'
        WHEN 'sleeve' THEN '袖丈'
        WHEN 'width' THEN '幅'
        WHEN 'leg-opening' THEN '裾幅'
        WHEN 'hip' THEN 'ヒップ'
        WHEN 'bottom-width' THEN '裾幅'
        WHEN 'depth' THEN '奥行き'
        WHEN 'start-fit-length' THEN 'フィット開始丈'
        WHEN 'end-fit-length' THEN 'フィット終了丈'
        WHEN 'height' THEN '高さ'
    END
FROM measurement_name mn
CROSS JOIN language l
WHERE l.code = 'ja';

-- Chinese translations
INSERT INTO measurement_name_translation (measurement_name_id, language_id, name)
SELECT 
    mn.id,
    l.id,
    CASE mn.name
        WHEN 'waist' THEN '腰围'
        WHEN 'inseam' THEN '内缝长'
        WHEN 'length' THEN '长度'
        WHEN 'rise' THEN '前裆长'
        WHEN 'hips' THEN '臀围'
        WHEN 'shoulders' THEN '肩宽'
        WHEN 'chest' THEN '胸围'
        WHEN 'sleeve' THEN '袖长'
        WHEN 'width' THEN '宽度'
        WHEN 'leg-opening' THEN '脚口'
        WHEN 'hip' THEN '臀部'
        WHEN 'bottom-width' THEN '下摆宽'
        WHEN 'depth' THEN '深度'
        WHEN 'start-fit-length' THEN '合身开始长度'
        WHEN 'end-fit-length' THEN '合身结束长度'
        WHEN 'height' THEN '高度'
    END
FROM measurement_name mn
CROSS JOIN language l
WHERE l.code = 'cn';

-- Korean translations
INSERT INTO measurement_name_translation (measurement_name_id, language_id, name)
SELECT 
    mn.id,
    l.id,
    CASE mn.name
        WHEN 'waist' THEN '허리'
        WHEN 'inseam' THEN '안솔기'
        WHEN 'length' THEN '길이'
        WHEN 'rise' THEN '밑위'
        WHEN 'hips' THEN '엉덩이'
        WHEN 'shoulders' THEN '어깨'
        WHEN 'chest' THEN '가슴'
        WHEN 'sleeve' THEN '소매'
        WHEN 'width' THEN '너비'
        WHEN 'leg-opening' THEN '밑단'
        WHEN 'hip' THEN '엉덩이'
        WHEN 'bottom-width' THEN '하단 너비'
        WHEN 'depth' THEN '깊이'
        WHEN 'start-fit-length' THEN '핏 시작 길이'
        WHEN 'end-fit-length' THEN '핏 끝 길이'
        WHEN 'height' THEN '높이'
    END
FROM measurement_name mn
CROSS JOIN language l
WHERE l.code = 'kr';

-- Insert comprehensive translations for all category names in all supported languages
-- English translations (already inserted above as default language)

-- French category translations
INSERT INTO category_translation (category_id, language_id, name)
SELECT DISTINCT
    c.id,
    l.id,
    CASE c.name
        -- Top level categories
        WHEN 'outerwear' THEN 'vêtements d''extérieur'
        WHEN 'tops' THEN 'hauts'
        WHEN 'bottoms' THEN 'bas'
        WHEN 'dresses' THEN 'robes'
        WHEN 'loungewear_sleepwear' THEN 'vêtements de détente et de nuit'
        WHEN 'accessories' THEN 'accessoires'
        WHEN 'shoes' THEN 'chaussures'
        WHEN 'bags' THEN 'sacs'
        WHEN 'objects' THEN 'objets'
        
        -- Outerwear
        WHEN 'jackets' THEN 'vestes'
        WHEN 'bomber' THEN 'bomber'
        WHEN 'leather' THEN 'cuir'
        WHEN 'puffer' THEN 'doudoune'
        WHEN 'rain' THEN 'imperméable'
        WHEN 'softshell' THEN 'softshell'
        WHEN 'hardshell' THEN 'hardshell'
        WHEN 'blazer' THEN 'blazer'
        WHEN 'coats' THEN 'manteaux'
        WHEN 'trench' THEN 'trench'
        WHEN 'peacoats' THEN 'caban'
        WHEN 'duvet' THEN 'duvet'
        WHEN 'parkas' THEN 'parkas'
        WHEN 'duffle' THEN 'duffle'
        WHEN 'vests' THEN 'gilets'
        WHEN 'fleece' THEN 'polaire'
        WHEN 'down' THEN 'duvet'
        WHEN 'cargo' THEN 'cargo'
        
        -- Tops
        WHEN 'shirts' THEN 'chemises'
        WHEN 'short_sleeve' THEN 'manches courtes'
        WHEN 'overshirts' THEN 'surchemises'
        WHEN 'linen' THEN 'lin'
        WHEN 'mesh' THEN 'maille'
        WHEN 'tshirts' THEN 't-shirts'
        WHEN 'crew_neck' THEN 'col rond'
        WHEN 'v_neck' THEN 'col V'
        WHEN 'graphic' THEN 'graphique'
        WHEN 'long_sleeve' THEN 'manches longues'
        WHEN 'pocket' THEN 'poche'
        WHEN 'sweaters_knits' THEN 'pulls et tricots'
        WHEN 'pullovers' THEN 'pulls'
        WHEN 'cardigans' THEN 'cardigans'
        WHEN 'turtlenecks' THEN 'cols roulés'
        WHEN 'lightweight' THEN 'léger'
        WHEN 'tanks' THEN 'débardeurs'
        WHEN 'hoodies_sweatshirts' THEN 'sweats à capuche'
        WHEN 'pullover' THEN 'pull'
        WHEN 'zip' THEN 'zip'
        WHEN 'crewneck' THEN 'col rond'
        WHEN 'cropped' THEN 'court'
        WHEN 'crop' THEN 'crop'
        
        -- Bottoms
        WHEN 'pants' THEN 'pantalons'
        WHEN 'trousers' THEN 'pantalons'
        WHEN 'drop_crotch' THEN 'entrejambe bas'
        WHEN 'joggers' THEN 'joggings'
        WHEN 'denim' THEN 'denim'
        WHEN 'chinos' THEN 'chinos'
        WHEN 'shorts' THEN 'shorts'
        WHEN 'athletic' THEN 'sport'
        WHEN 'skirts' THEN 'jupes'
        WHEN 'mini' THEN 'mini'
        WHEN 'midi' THEN 'midi'
        WHEN 'maxi' THEN 'maxi'
        WHEN 'pencil' THEN 'crayon'
        WHEN 'pleated' THEN 'plissée'
        WHEN 'wrap' THEN 'portefeuille'
        
        -- Dresses
        WHEN 'shirt' THEN 'chemise'
        
        -- Loungewear/Sleepwear
        WHEN 'boxers' THEN 'boxers'
        WHEN 'classic' THEN 'classique'
        WHEN 'boxer' THEN 'boxer'
        WHEN 'relaxed' THEN 'décontracté'
        WHEN 'bralettes' THEN 'bralettes'
        WHEN 'cotton' THEN 'coton'
        WHEN 'lace' THEN 'dentelle'
        WHEN 'sports' THEN 'sport'
        WHEN 'briefs' THEN 'culottes'
        WHEN 'robes' THEN 'peignoirs'
        WHEN 'waffle' THEN 'gaufre'
        WHEN 'belted' THEN 'ceinturé'
        WHEN 'swimwear_w' THEN 'maillots de bain femmes'
        WHEN 'swimwear_m' THEN 'maillots de bain hommes'
        
        -- Accessories
        WHEN 'jewelry' THEN 'bijoux'
        WHEN 'necklaces' THEN 'colliers'
        WHEN 'earrings' THEN 'boucles d''oreilles'
        WHEN 'rings' THEN 'bagues'
        WHEN 'bracelets' THEN 'bracelets'
        WHEN 'gloves' THEN 'gants'
        WHEN 'fingerless' THEN 'mitaines'
        WHEN 'mittens' THEN 'moufles'
        WHEN 'hats' THEN 'chapeaux'
        WHEN 'beanies' THEN 'bonnets'
        WHEN 'caps' THEN 'casquettes'
        WHEN 'panama' THEN 'panama'
        WHEN 'bucket' THEN 'bob'
        WHEN 'sun' THEN 'soleil'
        WHEN 'socks' THEN 'chaussettes'
        WHEN 'crew' THEN 'mi-mollet'
        WHEN 'ankle' THEN 'cheville'
        WHEN 'knee_high' THEN 'hautes'
        WHEN 'belts' THEN 'ceintures'
        WHEN 'scarves' THEN 'foulards'
        WHEN 'silk' THEN 'soie'
        WHEN 'cashmere' THEN 'cachemire'
        WHEN 'bandanas' THEN 'bandanas'
        WHEN 'shawls' THEN 'châles'
        
        -- Shoes
        WHEN 'boots' THEN 'bottes'
        WHEN 'ankle' THEN 'cheville'
        WHEN 'tall' THEN 'hautes'
        WHEN 'mid_calf' THEN 'mi-mollet'
        WHEN 'heels' THEN 'talons'
        WHEN 'flats' THEN 'plates'
        WHEN 'ballerina' THEN 'ballerines'
        WHEN 'lace_ups' THEN 'lacets'
        WHEN 'slippers_loafers' THEN 'mocassins'
        WHEN 'sneakers' THEN 'baskets'
        WHEN 'high_top' THEN 'montantes'
        WHEN 'low_top' THEN 'basses'
        WHEN 'wedge' THEN 'compensées'
        WHEN 'sandals' THEN 'sandales'
        WHEN 'flat' THEN 'plates'
        WHEN 'heeled' THEN 'à talons'
        
        -- Bags
        WHEN 'backpacks' THEN 'sacs à dos'
        WHEN 'handle' THEN 'poignée'
        WHEN 'shoulder' THEN 'épaule'
        WHEN 'tote' THEN 'cabas'
        
        -- Objects
        WHEN 'home' THEN 'maison'
        WHEN 'body' THEN 'corps'
        WHEN 'other' THEN 'autre'
        
        -- Common attributes
        WHEN 'width' THEN 'largeur'
        
        ELSE c.name
    END
FROM category c
CROSS JOIN language l
WHERE l.code = 'fr';

-- German category translations
INSERT INTO category_translation (category_id, language_id, name)
SELECT DISTINCT
    c.id,
    l.id,
    CASE c.name
        -- Top level categories
        WHEN 'outerwear' THEN 'oberbekleidung'
        WHEN 'tops' THEN 'oberteile'
        WHEN 'bottoms' THEN 'unterteile'
        WHEN 'dresses' THEN 'kleider'
        WHEN 'loungewear_sleepwear' THEN 'lounge- und nachtwäsche'
        WHEN 'accessories' THEN 'accessoires'
        WHEN 'shoes' THEN 'schuhe'
        WHEN 'bags' THEN 'taschen'
        WHEN 'objects' THEN 'objekte'
        
        -- Outerwear
        WHEN 'jackets' THEN 'jacken'
        WHEN 'bomber' THEN 'bomberjacke'
        WHEN 'leather' THEN 'leder'
        WHEN 'puffer' THEN 'daunenjacke'
        WHEN 'rain' THEN 'regenjacke'
        WHEN 'softshell' THEN 'softshell'
        WHEN 'hardshell' THEN 'hardshell'
        WHEN 'blazer' THEN 'blazer'
        WHEN 'coats' THEN 'mäntel'
        WHEN 'trench' THEN 'trenchcoat'
        WHEN 'peacoats' THEN 'peacoat'
        WHEN 'duvet' THEN 'daunenmantel'
        WHEN 'parkas' THEN 'parkas'
        WHEN 'duffle' THEN 'dufflecoat'
        WHEN 'vests' THEN 'westen'
        WHEN 'fleece' THEN 'fleece'
        WHEN 'down' THEN 'daunen'
        WHEN 'cargo' THEN 'cargo'
        
        -- Tops
        WHEN 'shirts' THEN 'hemden'
        WHEN 'short_sleeve' THEN 'kurzarm'
        WHEN 'overshirts' THEN 'overshirts'
        WHEN 'linen' THEN 'leinen'
        WHEN 'mesh' THEN 'netz'
        WHEN 'tshirts' THEN 't-shirts'
        WHEN 'crew_neck' THEN 'rundhals'
        WHEN 'v_neck' THEN 'v-ausschnitt'
        WHEN 'graphic' THEN 'grafik'
        WHEN 'long_sleeve' THEN 'langarm'
        WHEN 'pocket' THEN 'tasche'
        WHEN 'sweaters_knits' THEN 'pullover und strick'
        WHEN 'pullovers' THEN 'pullover'
        WHEN 'cardigans' THEN 'cardigans'
        WHEN 'turtlenecks' THEN 'rollkragen'
        WHEN 'lightweight' THEN 'leicht'
        WHEN 'tanks' THEN 'tank tops'
        WHEN 'hoodies_sweatshirts' THEN 'hoodies und sweatshirts'
        WHEN 'pullover' THEN 'pullover'
        WHEN 'zip' THEN 'reißverschluss'
        WHEN 'crewneck' THEN 'rundhals'
        WHEN 'cropped' THEN 'verkürzt'
        WHEN 'crop' THEN 'crop'
        
        -- Bottoms
        WHEN 'pants' THEN 'hosen'
        WHEN 'trousers' THEN 'hosen'
        WHEN 'drop_crotch' THEN 'tiefer schritt'
        WHEN 'joggers' THEN 'jogginghosen'
        WHEN 'denim' THEN 'denim'
        WHEN 'chinos' THEN 'chinos'
        WHEN 'shorts' THEN 'shorts'
        WHEN 'athletic' THEN 'sport'
        WHEN 'skirts' THEN 'röcke'
        WHEN 'mini' THEN 'mini'
        WHEN 'midi' THEN 'midi'
        WHEN 'maxi' THEN 'maxi'
        WHEN 'pencil' THEN 'bleistift'
        WHEN 'pleated' THEN 'plissiert'
        WHEN 'wrap' THEN 'wickel'
        
        -- Dresses
        WHEN 'shirt' THEN 'hemd'
        
        -- Loungewear/Sleepwear
        WHEN 'boxers' THEN 'boxershorts'
        WHEN 'classic' THEN 'klassisch'
        WHEN 'boxer' THEN 'boxer'
        WHEN 'relaxed' THEN 'entspannt'
        WHEN 'bralettes' THEN 'bralettes'
        WHEN 'cotton' THEN 'baumwolle'
        WHEN 'lace' THEN 'spitze'
        WHEN 'sports' THEN 'sport'
        WHEN 'briefs' THEN 'slips'
        WHEN 'robes' THEN 'bademäntel'
        WHEN 'waffle' THEN 'waffel'
        WHEN 'belted' THEN 'gegürtet'
        WHEN 'swimwear_w' THEN 'bademode damen'
        WHEN 'swimwear_m' THEN 'bademode herren'
        
        -- Accessories
        WHEN 'jewelry' THEN 'schmuck'
        WHEN 'necklaces' THEN 'halsketten'
        WHEN 'earrings' THEN 'ohrringe'
        WHEN 'rings' THEN 'ringe'
        WHEN 'bracelets' THEN 'armbänder'
        WHEN 'gloves' THEN 'handschuhe'
        WHEN 'fingerless' THEN 'fingerlos'
        WHEN 'mittens' THEN 'fäustlinge'
        WHEN 'hats' THEN 'hüte'
        WHEN 'beanies' THEN 'mützen'
        WHEN 'caps' THEN 'kappen'
        WHEN 'panama' THEN 'panama'
        WHEN 'bucket' THEN 'bucket'
        WHEN 'sun' THEN 'sonne'
        WHEN 'socks' THEN 'socken'
        WHEN 'crew' THEN 'crew'
        WHEN 'ankle' THEN 'knöchel'
        WHEN 'knee_high' THEN 'kniehoch'
        WHEN 'belts' THEN 'gürtel'
        WHEN 'scarves' THEN 'schals'
        WHEN 'silk' THEN 'seide'
        WHEN 'cashmere' THEN 'kaschmir'
        WHEN 'bandanas' THEN 'bandanas'
        WHEN 'shawls' THEN 'tücher'
        
        -- Shoes
        WHEN 'boots' THEN 'stiefel'
        WHEN 'ankle' THEN 'knöchel'
        WHEN 'tall' THEN 'hoch'
        WHEN 'mid_calf' THEN 'wadenhoch'
        WHEN 'heels' THEN 'absätze'
        WHEN 'flats' THEN 'flach'
        WHEN 'ballerina' THEN 'ballerinas'
        WHEN 'lace_ups' THEN 'schnürschuhe'
        WHEN 'slippers_loafers' THEN 'slipper'
        WHEN 'sneakers' THEN 'sneaker'
        WHEN 'high_top' THEN 'hoch'
        WHEN 'low_top' THEN 'niedrig'
        WHEN 'wedge' THEN 'keil'
        WHEN 'sandals' THEN 'sandalen'
        WHEN 'flat' THEN 'flach'
        WHEN 'heeled' THEN 'mit absatz'
        
        -- Bags
        WHEN 'backpacks' THEN 'rucksäcke'
        WHEN 'handle' THEN 'griff'
        WHEN 'shoulder' THEN 'schulter'
        WHEN 'tote' THEN 'shopper'
        
        -- Objects
        WHEN 'home' THEN 'heim'
        WHEN 'body' THEN 'körper'
        WHEN 'other' THEN 'andere'
        
        -- Common attributes
        WHEN 'width' THEN 'breite'
        
        ELSE c.name
    END
FROM category c
CROSS JOIN language l
WHERE l.code = 'de';

-- Italian category translations
INSERT INTO category_translation (category_id, language_id, name)
SELECT DISTINCT
    c.id,
    l.id,
    CASE c.name
        -- Top level categories
        WHEN 'outerwear' THEN 'capispalla'
        WHEN 'tops' THEN 'top'
        WHEN 'bottoms' THEN 'pantaloni'
        WHEN 'dresses' THEN 'vestiti'
        WHEN 'loungewear_sleepwear' THEN 'abbigliamento casa e notte'
        WHEN 'accessories' THEN 'accessori'
        WHEN 'shoes' THEN 'scarpe'
        WHEN 'bags' THEN 'borse'
        WHEN 'objects' THEN 'oggetti'
        
        -- Outerwear
        WHEN 'jackets' THEN 'giacche'
        WHEN 'bomber' THEN 'bomber'
        WHEN 'leather' THEN 'pelle'
        WHEN 'puffer' THEN 'piumino'
        WHEN 'rain' THEN 'impermeabile'
        WHEN 'softshell' THEN 'softshell'
        WHEN 'hardshell' THEN 'hardshell'
        WHEN 'blazer' THEN 'blazer'
        WHEN 'coats' THEN 'cappotti'
        WHEN 'trench' THEN 'trench'
        WHEN 'peacoats' THEN 'peacoat'
        WHEN 'duvet' THEN 'piumone'
        WHEN 'parkas' THEN 'parka'
        WHEN 'duffle' THEN 'montgomery'
        WHEN 'vests' THEN 'gilet'
        WHEN 'fleece' THEN 'pile'
        WHEN 'down' THEN 'piuma'
        WHEN 'cargo' THEN 'cargo'
        
        -- Tops
        WHEN 'shirts' THEN 'camicie'
        WHEN 'short_sleeve' THEN 'maniche corte'
        WHEN 'overshirts' THEN 'sovracamicie'
        WHEN 'linen' THEN 'lino'
        WHEN 'mesh' THEN 'rete'
        WHEN 'tshirts' THEN 't-shirt'
        WHEN 'crew_neck' THEN 'girocollo'
        WHEN 'v_neck' THEN 'scollo a v'
        WHEN 'graphic' THEN 'grafica'
        WHEN 'long_sleeve' THEN 'maniche lunghe'
        WHEN 'pocket' THEN 'tasca'
        WHEN 'sweaters_knits' THEN 'maglioni e maglieria'
        WHEN 'pullovers' THEN 'pullover'
        WHEN 'cardigans' THEN 'cardigan'
        WHEN 'turtlenecks' THEN 'collo alto'
        WHEN 'lightweight' THEN 'leggero'
        WHEN 'tanks' THEN 'canotte'
        WHEN 'hoodies_sweatshirts' THEN 'felpe'
        WHEN 'pullover' THEN 'pullover'
        WHEN 'zip' THEN 'zip'
        WHEN 'crewneck' THEN 'girocollo'
        WHEN 'cropped' THEN 'corto'
        WHEN 'crop' THEN 'crop'
        
        -- Bottoms
        WHEN 'pants' THEN 'pantaloni'
        WHEN 'trousers' THEN 'pantaloni'
        WHEN 'drop_crotch' THEN 'cavallo basso'
        WHEN 'joggers' THEN 'joggers'
        WHEN 'denim' THEN 'denim'
        WHEN 'chinos' THEN 'chinos'
        WHEN 'shorts' THEN 'shorts'
        WHEN 'athletic' THEN 'sportivo'
        WHEN 'skirts' THEN 'gonne'
        WHEN 'mini' THEN 'mini'
        WHEN 'midi' THEN 'midi'
        WHEN 'maxi' THEN 'maxi'
        WHEN 'pencil' THEN 'tubino'
        WHEN 'pleated' THEN 'plissettata'
        WHEN 'wrap' THEN 'portafoglio'
        
        -- Dresses
        WHEN 'shirt' THEN 'camicia'
        
        -- Loungewear/Sleepwear
        WHEN 'boxers' THEN 'boxer'
        WHEN 'classic' THEN 'classico'
        WHEN 'boxer' THEN 'boxer'
        WHEN 'relaxed' THEN 'rilassato'
        WHEN 'bralettes' THEN 'bralette'
        WHEN 'cotton' THEN 'cotone'
        WHEN 'lace' THEN 'pizzo'
        WHEN 'sports' THEN 'sport'
        WHEN 'briefs' THEN 'slip'
        WHEN 'robes' THEN 'accappatoi'
        WHEN 'waffle' THEN 'nido d''ape'
        WHEN 'belted' THEN 'con cintura'
        WHEN 'swimwear_w' THEN 'costumi da bagno donna'
        WHEN 'swimwear_m' THEN 'costumi da bagno uomo'
        
        -- Accessories
        WHEN 'jewelry' THEN 'gioielli'
        WHEN 'necklaces' THEN 'collane'
        WHEN 'earrings' THEN 'orecchini'
        WHEN 'rings' THEN 'anelli'
        WHEN 'bracelets' THEN 'braccialetti'
        WHEN 'gloves' THEN 'guanti'
        WHEN 'fingerless' THEN 'senza dita'
        WHEN 'mittens' THEN 'muffole'
        WHEN 'hats' THEN 'cappelli'
        WHEN 'beanies' THEN 'berretti'
        WHEN 'caps' THEN 'cappellini'
        WHEN 'panama' THEN 'panama'
        WHEN 'bucket' THEN 'bucket'
        WHEN 'sun' THEN 'sole'
        WHEN 'socks' THEN 'calze'
        WHEN 'crew' THEN 'crew'
        WHEN 'ankle' THEN 'caviglia'
        WHEN 'knee_high' THEN 'al ginocchio'
        WHEN 'belts' THEN 'cinture'
        WHEN 'scarves' THEN 'sciarpe'
        WHEN 'silk' THEN 'seta'
        WHEN 'cashmere' THEN 'cashmere'
        WHEN 'bandanas' THEN 'bandane'
        WHEN 'shawls' THEN 'scialli'
        
        -- Shoes
        WHEN 'boots' THEN 'stivali'
        WHEN 'ankle' THEN 'caviglia'
        WHEN 'tall' THEN 'alti'
        WHEN 'mid_calf' THEN 'metà polpaccio'
        WHEN 'heels' THEN 'tacchi'
        WHEN 'flats' THEN 'ballerine'
        WHEN 'ballerina' THEN 'ballerine'
        WHEN 'lace_ups' THEN 'stringate'
        WHEN 'slippers_loafers' THEN 'mocassini'
        WHEN 'sneakers' THEN 'sneakers'
        WHEN 'high_top' THEN 'alte'
        WHEN 'low_top' THEN 'basse'
        WHEN 'wedge' THEN 'zeppa'
        WHEN 'sandals' THEN 'sandali'
        WHEN 'flat' THEN 'piatti'
        WHEN 'heeled' THEN 'con tacco'
        
        -- Bags
        WHEN 'backpacks' THEN 'zaini'
        WHEN 'handle' THEN 'manico'
        WHEN 'shoulder' THEN 'spalla'
        WHEN 'tote' THEN 'shopper'
        
        -- Objects
        WHEN 'home' THEN 'casa'
        WHEN 'body' THEN 'corpo'
        WHEN 'other' THEN 'altro'
        
        -- Common attributes
        WHEN 'width' THEN 'larghezza'
        
        ELSE c.name
    END
FROM category c
CROSS JOIN language l
WHERE l.code = 'it';

-- Japanese category translations
INSERT INTO category_translation (category_id, language_id, name)
SELECT DISTINCT
    c.id,
    l.id,
    CASE c.name
        -- Top level categories
        WHEN 'outerwear' THEN 'アウター'
        WHEN 'tops' THEN 'トップス'
        WHEN 'bottoms' THEN 'ボトムス'
        WHEN 'dresses' THEN 'ドレス'
        WHEN 'loungewear_sleepwear' THEN 'ラウンジウェア・ナイトウェア'
        WHEN 'accessories' THEN 'アクセサリー'
        WHEN 'shoes' THEN '靴'
        WHEN 'bags' THEN 'バッグ'
        WHEN 'objects' THEN 'オブジェクト'
        
        -- Outerwear
        WHEN 'jackets' THEN 'ジャケット'
        WHEN 'bomber' THEN 'ボンバー'
        WHEN 'leather' THEN 'レザー'
        WHEN 'puffer' THEN 'ダウン'
        WHEN 'rain' THEN 'レイン'
        WHEN 'softshell' THEN 'ソフトシェル'
        WHEN 'hardshell' THEN 'ハードシェル'
        WHEN 'blazer' THEN 'ブレザー'
        WHEN 'coats' THEN 'コート'
        WHEN 'trench' THEN 'トレンチ'
        WHEN 'peacoats' THEN 'ピーコート'
        WHEN 'duvet' THEN 'ダウンコート'
        WHEN 'parkas' THEN 'パーカー'
        WHEN 'duffle' THEN 'ダッフル'
        WHEN 'vests' THEN 'ベスト'
        WHEN 'fleece' THEN 'フリース'
        WHEN 'down' THEN 'ダウン'
        WHEN 'cargo' THEN 'カーゴ'
        
        -- Tops
        WHEN 'shirts' THEN 'シャツ'
        WHEN 'short_sleeve' THEN '半袖'
        WHEN 'overshirts' THEN 'オーバーシャツ'
        WHEN 'linen' THEN 'リネン'
        WHEN 'mesh' THEN 'メッシュ'
        WHEN 'tshirts' THEN 'Tシャツ'
        WHEN 'crew_neck' THEN 'クルーネック'
        WHEN 'v_neck' THEN 'Vネック'
        WHEN 'graphic' THEN 'グラフィック'
        WHEN 'long_sleeve' THEN '長袖'
        WHEN 'pocket' THEN 'ポケット'
        WHEN 'sweaters_knits' THEN 'セーター・ニット'
        WHEN 'pullovers' THEN 'プルオーバー'
        WHEN 'cardigans' THEN 'カーディガン'
        WHEN 'turtlenecks' THEN 'タートルネック'
        WHEN 'lightweight' THEN '軽量'
        WHEN 'tanks' THEN 'タンクトップ'
        WHEN 'hoodies_sweatshirts' THEN 'パーカー・スウェット'
        WHEN 'pullover' THEN 'プルオーバー'
        WHEN 'zip' THEN 'ジップ'
        WHEN 'crewneck' THEN 'クルーネック'
        WHEN 'cropped' THEN 'クロップド'
        WHEN 'crop' THEN 'クロップ'
        
        -- Bottoms
        WHEN 'pants' THEN 'パンツ'
        WHEN 'trousers' THEN 'トラウザー'
        WHEN 'drop_crotch' THEN 'ドロップクロッチ'
        WHEN 'joggers' THEN 'ジョガー'
        WHEN 'denim' THEN 'デニム'
        WHEN 'chinos' THEN 'チノ'
        WHEN 'shorts' THEN 'ショーツ'
        WHEN 'athletic' THEN 'アスレチック'
        WHEN 'skirts' THEN 'スカート'
        WHEN 'mini' THEN 'ミニ'
        WHEN 'midi' THEN 'ミディ'
        WHEN 'maxi' THEN 'マキシ'
        WHEN 'pencil' THEN 'ペンシル'
        WHEN 'pleated' THEN 'プリーツ'
        WHEN 'wrap' THEN 'ラップ'
        
        -- Dresses
        WHEN 'shirt' THEN 'シャツ'
        
        -- Loungewear/Sleepwear
        WHEN 'boxers' THEN 'ボクサー'
        WHEN 'classic' THEN 'クラシック'
        WHEN 'boxer' THEN 'ボクサー'
        WHEN 'relaxed' THEN 'リラックス'
        WHEN 'bralettes' THEN 'ブラレット'
        WHEN 'cotton' THEN 'コットン'
        WHEN 'lace' THEN 'レース'
        WHEN 'sports' THEN 'スポーツ'
        WHEN 'briefs' THEN 'ブリーフ'
        WHEN 'robes' THEN 'ローブ'
        WHEN 'waffle' THEN 'ワッフル'
        WHEN 'belted' THEN 'ベルト付き'
        WHEN 'swimwear_w' THEN '水着（レディース）'
        WHEN 'swimwear_m' THEN '水着（メンズ）'
        
        -- Accessories
        WHEN 'jewelry' THEN 'ジュエリー'
        WHEN 'necklaces' THEN 'ネックレス'
        WHEN 'earrings' THEN 'イヤリング'
        WHEN 'rings' THEN 'リング'
        WHEN 'bracelets' THEN 'ブレスレット'
        WHEN 'gloves' THEN 'グローブ'
        WHEN 'fingerless' THEN 'フィンガーレス'
        WHEN 'mittens' THEN 'ミトン'
        WHEN 'hats' THEN 'ハット'
        WHEN 'beanies' THEN 'ビーニー'
        WHEN 'caps' THEN 'キャップ'
        WHEN 'panama' THEN 'パナマ'
        WHEN 'bucket' THEN 'バケット'
        WHEN 'sun' THEN 'サン'
        WHEN 'socks' THEN 'ソックス'
        WHEN 'crew' THEN 'クルー'
        WHEN 'ankle' THEN 'アンクル'
        WHEN 'knee_high' THEN 'ニーハイ'
        WHEN 'belts' THEN 'ベルト'
        WHEN 'scarves' THEN 'スカーフ'
        WHEN 'silk' THEN 'シルク'
        WHEN 'cashmere' THEN 'カシミア'
        WHEN 'bandanas' THEN 'バンダナ'
        WHEN 'shawls' THEN 'ショール'
        
        -- Shoes
        WHEN 'boots' THEN 'ブーツ'
        WHEN 'ankle' THEN 'アンクル'
        WHEN 'tall' THEN 'トール'
        WHEN 'mid_calf' THEN 'ミドルカーフ'
        WHEN 'heels' THEN 'ヒール'
        WHEN 'flats' THEN 'フラット'
        WHEN 'ballerina' THEN 'バレリーナ'
        WHEN 'lace_ups' THEN 'レースアップ'
        WHEN 'slippers_loafers' THEN 'スリッパ・ローファー'
        WHEN 'sneakers' THEN 'スニーカー'
        WHEN 'high_top' THEN 'ハイトップ'
        WHEN 'low_top' THEN 'ロートップ'
        WHEN 'wedge' THEN 'ウェッジ'
        WHEN 'sandals' THEN 'サンダル'
        WHEN 'flat' THEN 'フラット'
        WHEN 'heeled' THEN 'ヒール付き'
        
        -- Bags
        WHEN 'backpacks' THEN 'バックパック'
        WHEN 'handle' THEN 'ハンドル'
        WHEN 'shoulder' THEN 'ショルダー'
        WHEN 'tote' THEN 'トート'
        
        -- Objects
        WHEN 'home' THEN 'ホーム'
        WHEN 'body' THEN 'ボディ'
        WHEN 'other' THEN 'その他'
        
        -- Common attributes
        WHEN 'width' THEN '幅'
        
        ELSE c.name
    END
FROM category c
CROSS JOIN language l
WHERE l.code = 'ja';

-- Chinese category translations
INSERT INTO category_translation (category_id, language_id, name)
SELECT DISTINCT
    c.id,
    l.id,
    CASE c.name
        -- Top level categories
        WHEN 'outerwear' THEN '外套'
        WHEN 'tops' THEN '上装'
        WHEN 'bottoms' THEN '下装'
        WHEN 'dresses' THEN '连衣裙'
        WHEN 'loungewear_sleepwear' THEN '居家服和睡衣'
        WHEN 'accessories' THEN '配饰'
        WHEN 'shoes' THEN '鞋'
        WHEN 'bags' THEN '包'
        WHEN 'objects' THEN '物品'
        
        -- Outerwear
        WHEN 'jackets' THEN '夹克'
        WHEN 'bomber' THEN '飞行员夹克'
        WHEN 'leather' THEN '皮革'
        WHEN 'puffer' THEN '羽绒服'
        WHEN 'rain' THEN '雨衣'
        WHEN 'softshell' THEN '软壳'
        WHEN 'hardshell' THEN '硬壳'
        WHEN 'blazer' THEN '西装外套'
        WHEN 'coats' THEN '大衣'
        WHEN 'trench' THEN '风衣'
        WHEN 'peacoats' THEN '海军大衣'
        WHEN 'duvet' THEN '羽绒大衣'
        WHEN 'parkas' THEN '派克大衣'
        WHEN 'duffle' THEN '牛角扣大衣'
        WHEN 'vests' THEN '马甲'
        WHEN 'fleece' THEN '抓绒'
        WHEN 'down' THEN '羽绒'
        WHEN 'cargo' THEN '工装'
        
        -- Tops
        WHEN 'shirts' THEN '衬衫'
        WHEN 'short_sleeve' THEN '短袖'
        WHEN 'overshirts' THEN '外穿衬衫'
        WHEN 'linen' THEN '亚麻'
        WHEN 'mesh' THEN '网眼'
        WHEN 'tshirts' THEN 'T恤'
        WHEN 'crew_neck' THEN '圆领'
        WHEN 'v_neck' THEN 'V领'
        WHEN 'graphic' THEN '图案'
        WHEN 'long_sleeve' THEN '长袖'
        WHEN 'pocket' THEN '口袋'
        WHEN 'sweaters_knits' THEN '毛衣针织'
        WHEN 'pullovers' THEN '套头衫'
        WHEN 'cardigans' THEN '开襟衫'
        WHEN 'turtlenecks' THEN '高领'
        WHEN 'lightweight' THEN '轻薄'
        WHEN 'tanks' THEN '背心'
        WHEN 'hoodies_sweatshirts' THEN '连帽衫和卫衣'
        WHEN 'pullover' THEN '套头衫'
        WHEN 'zip' THEN '拉链'
        WHEN 'crewneck' THEN '圆领'
        WHEN 'cropped' THEN '短款'
        WHEN 'crop' THEN '露脐'
        
        -- Bottoms
        WHEN 'pants' THEN '裤子'
        WHEN 'trousers' THEN '长裤'
        WHEN 'drop_crotch' THEN '低档'
        WHEN 'joggers' THEN '慢跑裤'
        WHEN 'denim' THEN '牛仔'
        WHEN 'chinos' THEN '休闲裤'
        WHEN 'shorts' THEN '短裤'
        WHEN 'athletic' THEN '运动'
        WHEN 'skirts' THEN '裙子'
        WHEN 'mini' THEN '迷你'
        WHEN 'midi' THEN '中长'
        WHEN 'maxi' THEN '长款'
        WHEN 'pencil' THEN '铅笔裙'
        WHEN 'pleated' THEN '百褶'
        WHEN 'wrap' THEN '包裹式'
        
        -- Dresses
        WHEN 'shirt' THEN '衬衫裙'
        
        -- Loungewear/Sleepwear
        WHEN 'boxers' THEN '四角裤'
        WHEN 'classic' THEN '经典'
        WHEN 'boxer' THEN '四角裤'
        WHEN 'relaxed' THEN '宽松'
        WHEN 'bralettes' THEN '无钢圈文胸'
        WHEN 'cotton' THEN '棉质'
        WHEN 'lace' THEN '蕾丝'
        WHEN 'sports' THEN '运动'
        WHEN 'briefs' THEN '三角裤'
        WHEN 'robes' THEN '浴袍'
        WHEN 'waffle' THEN '华夫格'
        WHEN 'belted' THEN '腰带式'
        WHEN 'swimwear_w' THEN '女式泳装'
        WHEN 'swimwear_m' THEN '男式泳装'
        
        -- Accessories
        WHEN 'jewelry' THEN '珠宝'
        WHEN 'necklaces' THEN '项链'
        WHEN 'earrings' THEN '耳环'
        WHEN 'rings' THEN '戒指'
        WHEN 'bracelets' THEN '手链'
        WHEN 'gloves' THEN '手套'
        WHEN 'fingerless' THEN '无指手套'
        WHEN 'mittens' THEN '连指手套'
        WHEN 'hats' THEN '帽子'
        WHEN 'beanies' THEN '毛线帽'
        WHEN 'caps' THEN '帽子'
        WHEN 'panama' THEN '巴拿马帽'
        WHEN 'bucket' THEN '渔夫帽'
        WHEN 'sun' THEN '太阳帽'
        WHEN 'socks' THEN '袜子'
        WHEN 'crew' THEN '中筒'
        WHEN 'ankle' THEN '踝袜'
        WHEN 'knee_high' THEN '及膝'
        WHEN 'belts' THEN '腰带'
        WHEN 'scarves' THEN '围巾'
        WHEN 'silk' THEN '丝绸'
        WHEN 'cashmere' THEN '羊绒'
        WHEN 'bandanas' THEN '头巾'
        WHEN 'shawls' THEN '披肩'
        
        -- Shoes
        WHEN 'boots' THEN '靴子'
        WHEN 'ankle' THEN '踝靴'
        WHEN 'tall' THEN '高筒'
        WHEN 'mid_calf' THEN '中筒'
        WHEN 'heels' THEN '高跟鞋'
        WHEN 'flats' THEN '平底鞋'
        WHEN 'ballerina' THEN '芭蕾舞鞋'
        WHEN 'lace_ups' THEN '系带鞋'
        WHEN 'slippers_loafers' THEN '拖鞋乐福鞋'
        WHEN 'sneakers' THEN '运动鞋'
        WHEN 'high_top' THEN '高帮'
        WHEN 'low_top' THEN '低帮'
        WHEN 'wedge' THEN '楔形'
        WHEN 'sandals' THEN '凉鞋'
        WHEN 'flat' THEN '平底'
        WHEN 'heeled' THEN '有跟'
        
        -- Bags
        WHEN 'backpacks' THEN '背包'
        WHEN 'handle' THEN '手提包'
        WHEN 'shoulder' THEN '肩包'
        WHEN 'tote' THEN '托特包'
        
        -- Objects
        WHEN 'home' THEN '家居'
        WHEN 'body' THEN '身体'
        WHEN 'other' THEN '其他'
        
        -- Common attributes
        WHEN 'width' THEN '宽度'
        
        ELSE c.name
    END
FROM category c
CROSS JOIN language l
WHERE l.code = 'cn';

-- Korean category translations
INSERT INTO category_translation (category_id, language_id, name)
SELECT DISTINCT
    c.id,
    l.id,
    CASE c.name
        -- Top level categories
        WHEN 'outerwear' THEN '아우터'
        WHEN 'tops' THEN '상의'
        WHEN 'bottoms' THEN '하의'
        WHEN 'dresses' THEN '드레스'
        WHEN 'loungewear_sleepwear' THEN '라운지웨어 및 잠옷'
        WHEN 'accessories' THEN '액세서리'
        WHEN 'shoes' THEN '신발'
        WHEN 'bags' THEN '가방'
        WHEN 'objects' THEN '오브젝트'
        
        -- Outerwear
        WHEN 'jackets' THEN '재킷'
        WHEN 'bomber' THEN '봄버재킷'
        WHEN 'leather' THEN '가죽'
        WHEN 'puffer' THEN '패딩'
        WHEN 'rain' THEN '레인코트'
        WHEN 'softshell' THEN '소프트셸'
        WHEN 'hardshell' THEN '하드셸'
        WHEN 'blazer' THEN '블레이저'
        WHEN 'coats' THEN '코트'
        WHEN 'trench' THEN '트렌치코트'
        WHEN 'peacoats' THEN '피코트'
        WHEN 'duvet' THEN '다운코트'
        WHEN 'parkas' THEN '파카'
        WHEN 'duffle' THEN '더플코트'
        WHEN 'vests' THEN '조끼'
        WHEN 'fleece' THEN '플리스'
        WHEN 'down' THEN '다운'
        WHEN 'cargo' THEN '카고'
        
        -- Tops
        WHEN 'shirts' THEN '셔츠'
        WHEN 'short_sleeve' THEN '반팔'
        WHEN 'overshirts' THEN '오버셔츠'
        WHEN 'linen' THEN '린넨'
        WHEN 'mesh' THEN '메시'
        WHEN 'tshirts' THEN '티셔츠'
        WHEN 'crew_neck' THEN '크루넥'
        WHEN 'v_neck' THEN '브이넥'
        WHEN 'graphic' THEN '그래픽'
        WHEN 'long_sleeve' THEN '긴팔'
        WHEN 'pocket' THEN '포켓'
        WHEN 'sweaters_knits' THEN '스웨터 및 니트'
        WHEN 'pullovers' THEN '풀오버'
        WHEN 'cardigans' THEN '가디건'
        WHEN 'turtlenecks' THEN '터틀넥'
        WHEN 'lightweight' THEN '라이트웨이트'
        WHEN 'tanks' THEN '탱크톱'
        WHEN 'hoodies_sweatshirts' THEN '후드티 및 맨투맨'
        WHEN 'pullover' THEN '풀오버'
        WHEN 'zip' THEN '지퍼'
        WHEN 'crewneck' THEN '크루넥'
        WHEN 'cropped' THEN '크롭'
        WHEN 'crop' THEN '크롭'
        
        -- Bottoms
        WHEN 'pants' THEN '바지'
        WHEN 'trousers' THEN '슬랙스'
        WHEN 'drop_crotch' THEN '드롭크로치'
        WHEN 'joggers' THEN '조거팬츠'
        WHEN 'denim' THEN '데님'
        WHEN 'chinos' THEN '치노'
        WHEN 'shorts' THEN '반바지'
        WHEN 'athletic' THEN '애슬레틱'
        WHEN 'skirts' THEN '스커트'
        WHEN 'mini' THEN '미니'
        WHEN 'midi' THEN '미디'
        WHEN 'maxi' THEN '맥시'
        WHEN 'pencil' THEN '펜슬'
        WHEN 'pleated' THEN '플리츠'
        WHEN 'wrap' THEN '랩'
        
        -- Dresses
        WHEN 'shirt' THEN '셔츠'
        
        -- Loungewear/Sleepwear
        WHEN 'boxers' THEN '박서팬츠'
        WHEN 'classic' THEN '클래식'
        WHEN 'boxer' THEN '박서'
        WHEN 'relaxed' THEN '릴렉스'
        WHEN 'bralettes' THEN '브라렛'
        WHEN 'cotton' THEN '코튼'
        WHEN 'lace' THEN '레이스'
        WHEN 'sports' THEN '스포츠'
        WHEN 'briefs' THEN '브리프'
        WHEN 'robes' THEN '가운'
        WHEN 'waffle' THEN '와플'
        WHEN 'belted' THEN '벨트형'
        WHEN 'swimwear_w' THEN '여성 수영복'
        WHEN 'swimwear_m' THEN '남성 수영복'
        
        -- Accessories
        WHEN 'jewelry' THEN '주얼리'
        WHEN 'necklaces' THEN '목걸이'
        WHEN 'earrings' THEN '귀걸이'
        WHEN 'rings' THEN '반지'
        WHEN 'bracelets' THEN '팔찌'
        WHEN 'gloves' THEN '장갑'
        WHEN 'fingerless' THEN '지퍼없는 장갑'
        WHEN 'mittens' THEN '벙어리장갑'
        WHEN 'hats' THEN '모자'
        WHEN 'beanies' THEN '비니'
        WHEN 'caps' THEN '캡'
        WHEN 'panama' THEN '파나마'
        WHEN 'bucket' THEN '버킷햇'
        WHEN 'sun' THEN '선햇'
        WHEN 'socks' THEN '양말'
        WHEN 'crew' THEN '크루'
        WHEN 'ankle' THEN '발목'
        WHEN 'knee_high' THEN '니하이'
        WHEN 'belts' THEN '벨트'
        WHEN 'scarves' THEN '스카프'
        WHEN 'silk' THEN '실크'
        WHEN 'cashmere' THEN '캐시미어'
        WHEN 'bandanas' THEN '반다나'
        WHEN 'shawls' THEN '숄'
        
        -- Shoes
        WHEN 'boots' THEN '부츠'
        WHEN 'ankle' THEN '앵클'
        WHEN 'tall' THEN '롱'
        WHEN 'mid_calf' THEN '미드카프'
        WHEN 'heels' THEN '힐'
        WHEN 'flats' THEN '플랫'
        WHEN 'ballerina' THEN '발레리나'
        WHEN 'lace_ups' THEN '레이스업'
        WHEN 'slippers_loafers' THEN '슬리퍼 로퍼'
        WHEN 'sneakers' THEN '스니커즈'
        WHEN 'high_top' THEN '하이탑'
        WHEN 'low_top' THEN '로우탑'
        WHEN 'wedge' THEN '웨지'
        WHEN 'sandals' THEN '샌들'
        WHEN 'flat' THEN '플랫'
        WHEN 'heeled' THEN '힐있는'
        
        -- Bags
        WHEN 'backpacks' THEN '백팩'
        WHEN 'handle' THEN '핸들백'
        WHEN 'shoulder' THEN '숄더백'
        WHEN 'tote' THEN '토트백'
        
        -- Objects
        WHEN 'home' THEN '홈'
        WHEN 'body' THEN '바디'
        WHEN 'other' THEN '기타'
        
        -- Common attributes
        WHEN 'width' THEN '너비'
        
        ELSE c.name
    END
FROM category c
CROSS JOIN language l
WHERE l.code = 'kr';

-- Remove translatable columns from main tables now that data is in translation tables
-- This normalizes the schema and prevents data duplication

-- Remove name and description columns from product table
ALTER TABLE product 
DROP COLUMN name,
DROP COLUMN description;

-- Remove heading and description columns from archive table  
ALTER TABLE archive
DROP COLUMN heading,
DROP COLUMN description;

-- Remove name column from category table (first drop the unique constraint that references it)
ALTER TABLE category DROP INDEX unique_name_parent;
ALTER TABLE category DROP COLUMN name;

-- Remove name column from measurement_name table
ALTER TABLE measurement_name DROP COLUMN name;

-- Update view_categories to use translation tables instead of removed name columns
-- This view now requires a language parameter or defaults to English
DROP VIEW IF EXISTS view_categories;

CREATE VIEW view_categories AS
SELECT 
    c1.id AS category_id,
    COALESCE(ct1.name, c1_fallback.name, 'Unknown') AS category_name,
    c1.level_id,
    cl.name AS level_name,
    c1.parent_id,
    COALESCE(ct2.name, c2_fallback.name, 'Unknown') AS parent_name,
    c1.created_at,
    c1.updated_at
FROM 
    category c1
LEFT JOIN 
    category c2 ON c1.parent_id = c2.id
JOIN 
    category_level cl ON c1.level_id = cl.id
LEFT JOIN
    category_translation ct1 ON c1.id = ct1.category_id 
    AND ct1.language_id = (SELECT id FROM language WHERE is_default = TRUE LIMIT 1)
LEFT JOIN
    category_translation ct2 ON c2.id = ct2.category_id 
    AND ct2.language_id = (SELECT id FROM language WHERE is_default = TRUE LIMIT 1)
LEFT JOIN
    category_translation c1_fallback ON c1.id = c1_fallback.category_id 
    AND c1_fallback.language_id = (SELECT id FROM language WHERE code = 'en' LIMIT 1)
LEFT JOIN
    category_translation c2_fallback ON c2.id = c2_fallback.category_id 
    AND c2_fallback.language_id = (SELECT id FROM language WHERE code = 'en' LIMIT 1);

-- Note: Hero translations will need to be handled separately as the hero table contains JSON data
-- and the specific hero entities need to be extracted and mapped to translation records
-- This should be done in application code or a separate data migration script

-- +migrate Down
-- Remove translation tables and restore original structure
-- WARNING: This will permanently delete all translation data

DROP TABLE IF EXISTS hero_featured_archive_translation;
DROP TABLE IF EXISTS hero_featured_products_tag_translation;
DROP TABLE IF EXISTS hero_featured_products_translation;
DROP TABLE IF EXISTS hero_single_translation;
DROP TABLE IF EXISTS measurement_name_translation;
DROP TABLE IF EXISTS category_translation;
DROP TABLE IF EXISTS archive_translation;
DROP TABLE IF EXISTS product_translation;
DROP TABLE IF EXISTS language;

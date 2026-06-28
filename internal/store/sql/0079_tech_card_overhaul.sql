-- +migrate Up
-- Tech card overhaul. The model shifts so the BOM becomes a pure material-article catalog
-- and the colourway becomes the "recipe": tech_card_colorway_usage binds a BOM article to a
-- garment part (placement), the colour it takes in that colourway, and its consumption
-- (per-garment and/or per-size). Construction description moves from flat header strings to
-- tech_card_detail (+ reference media). POM, header cost targets and labour pricing are
-- removed. Clean break (new feature, ~no production data); the BOM colour matrix is migrated
-- into usages BEFORE it is dropped, so existing per-colourway colours are preserved.
--
-- Auto-named CHECK drops below follow the established idiom (see 0073/0076): MySQL names an
-- inline column CHECK <table>_chk_<n> in column order at CREATE TABLE time, and a column that
-- a CHECK references cannot be dropped until the CHECK is dropped.

-- 1. Colourway material recipe.
CREATE TABLE tech_card_colorway_usage (
  id INT PRIMARY KEY AUTO_INCREMENT,
  colorway_id INT NOT NULL,
  bom_item_index INT NULL COMMENT '0-based index into the submitted bom_items; NULL = unset'
    CHECK (bom_item_index IS NULL OR bom_item_index >= 0),
  placement VARCHAR(255) NULL COMMENT 'garment part (normalised trim+lower); matched to operation.placement',
  color VARCHAR(255) NULL COMMENT 'material colour in this colourway',
  pantone VARCHAR(64) NULL COMMENT 'material Pantone / code in this colourway',
  consumption DECIMAL(10, 3) NULL COMMENT 'per-garment rate (measured materials)'
    CHECK (consumption IS NULL OR consumption >= 0),
  quantity DECIMAL(10, 3) NULL COMMENT 'count (countable trims)'
    CHECK (quantity IS NULL OR quantity >= 0),
  display_order INT NOT NULL DEFAULT 0,
  INDEX idx_tech_card_colorway_usage_colorway (colorway_id),
  FOREIGN KEY (colorway_id) REFERENCES tech_card_colorway(id) ON DELETE CASCADE
) COMMENT 'Per-colourway material recipe: which BOM article on which part, colour, consumption';

-- 2. Per-size consumption of a usage (graded fabric usage).
CREATE TABLE tech_card_colorway_usage_consumption (
  id INT PRIMARY KEY AUTO_INCREMENT,
  usage_id INT NOT NULL,
  size_id INT NOT NULL COMMENT 'FK size(id); one of the tech card size range',
  consumption DECIMAL(10, 3) NOT NULL COMMENT 'per-garment rate at this size'
    CHECK (consumption >= 0),
  display_order INT NOT NULL DEFAULT 0,
  UNIQUE KEY uniq_tech_card_colorway_usage_consumption (usage_id, size_id),
  FOREIGN KEY (usage_id) REFERENCES tech_card_colorway_usage(id) ON DELETE CASCADE,
  FOREIGN KEY (size_id) REFERENCES size(id)
) COMMENT 'Per-size consumption (норма расхода) of a colourway usage';

-- 3. Construction-description aspects (replaces the flat «Титул» strings).
CREATE TABLE tech_card_detail (
  id INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  detail_key VARCHAR(64) NULL COMMENT 'aspect name (silhouette/collar/…); freeform',
  detail_text TEXT NULL COMMENT 'the description for this aspect',
  display_order INT NOT NULL DEFAULT 0,
  INDEX idx_tech_card_detail_card (tech_card_id),
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) COMMENT 'Construction-description aspects (Sheet «Титул», lower block)';

-- 4. Reference media for a construction-description aspect.
CREATE TABLE tech_card_detail_media (
  id INT PRIMARY KEY AUTO_INCREMENT,
  detail_id INT NOT NULL,
  media_id INT NOT NULL COMMENT 'FK media(id)',
  display_order INT NOT NULL DEFAULT 0,
  INDEX idx_tech_card_detail_media_detail (detail_id),
  FOREIGN KEY (detail_id) REFERENCES tech_card_detail(id) ON DELETE CASCADE,
  FOREIGN KEY (media_id) REFERENCES media(id)
) COMMENT 'Reference media for a tech_card_detail aspect';

-- 5. Operations attach to a garment part; the real material resolves via the colourway usages.
ALTER TABLE tech_card_operation
  ADD COLUMN placement VARCHAR(255) NULL
    COMMENT 'garment part this operation works on (normalised); resolves material via the colourway usages';

-- 6. Migrate the BOM colour matrix into colourway usages BEFORE dropping it. Each matrix cell
--    becomes a usage under its colourway, carrying the BOM line's placement/consumption/quantity
--    and the cell's colour/pantone. bom_item_index is the BOM line's 0-based position within its
--    card (the same order the read path emits, display_order then id). Safe on empty source.
INSERT INTO tech_card_colorway_usage
    (colorway_id, bom_item_index, placement, color, pantone, consumption, quantity, display_order)
SELECT bc.colorway_id, idx.bom_item_index, bi.placement, bc.color, bc.pantone,
       bi.consumption, bi.quantity, bc.display_order
FROM tech_card_bom_colorway bc
JOIN tech_card_bom_item bi ON bi.id = bc.bom_item_id
JOIN (
  SELECT id, ROW_NUMBER() OVER (PARTITION BY tech_card_id ORDER BY display_order, id) - 1 AS bom_item_index
  FROM tech_card_bom_item
) idx ON idx.id = bi.id;

-- 7. BOM articles with no colour cell at all → one usage per colourway (no colour), so a fabric
--    used across all colourways without a per-colourway colour is still represented.
INSERT INTO tech_card_colorway_usage
    (colorway_id, bom_item_index, placement, color, pantone, consumption, quantity, display_order)
SELECT cw.id, idx.bom_item_index, bi.placement, NULL, NULL, bi.consumption, bi.quantity, 0
FROM tech_card_bom_item bi
JOIN tech_card_colorway cw ON cw.tech_card_id = bi.tech_card_id
JOIN (
  SELECT id, ROW_NUMBER() OVER (PARTITION BY tech_card_id ORDER BY display_order, id) - 1 AS bom_item_index
  FROM tech_card_bom_item
) idx ON idx.id = bi.id
WHERE NOT EXISTS (SELECT 1 FROM tech_card_bom_colorway bc WHERE bc.bom_item_id = bi.id);
-- NOTE: per-size BOM consumption (tech_card_bom_consumption) is NOT backfilled onto the new
-- usages, and the flat construction-description strings are NOT migrated into tech_card_detail
-- (trivial volume, clean cutover; details start empty — plan §5).

-- 8. Drop the obsolete child tables (colour matrix, per-size BOM consumption, POM).
DROP TABLE IF EXISTS tech_card_bom_consumption;
DROP TABLE IF EXISTS tech_card_bom_colorway;
DROP TABLE IF EXISTS tech_card_pom_actual;
DROP TABLE IF EXISTS tech_card_pom_grade;
DROP TABLE IF EXISTS tech_card_pom_point;

-- 9. BOM line → catalog article: drop placement/consumption/quantity (moved onto the usage).
--    consumption = tech_card_bom_item_chk_2, quantity = tech_card_bom_item_chk_3 (column order
--    in 0068; section was chk_1, renamed in 0076). placement has no CHECK.
ALTER TABLE tech_card_bom_item
  DROP CHECK tech_card_bom_item_chk_2,
  DROP CHECK tech_card_bom_item_chk_3;
ALTER TABLE tech_card_bom_item
  DROP COLUMN placement,
  DROP COLUMN consumption,
  DROP COLUMN quantity;

-- 10. Construction: drop labour pricing (labour_rate = tech_card_construction_chk_1; the only
--     CHECK on the table, added in 0072. labour_rate_currency has no CHECK).
ALTER TABLE tech_card_construction
  DROP CHECK tech_card_construction_chk_1;
ALTER TABLE tech_card_construction
  DROP COLUMN labour_rate,
  DROP COLUMN labour_rate_currency;

-- 11. Header: drop cost targets and the flat construction-description strings (now in
--     tech_card_detail). target_cost = tech_card_chk_4, target_retail_price = tech_card_chk_5
--     (column order in 0067: target_gender chk_1, stage chk_2, approval_state chk_3,
--     target_cost chk_4, target_retail_price chk_5, measurement_unit chk_6). The remaining
--     columns (currency + the description strings) have no CHECK.
ALTER TABLE tech_card
  DROP CHECK tech_card_chk_4,
  DROP CHECK tech_card_chk_5;
ALTER TABLE tech_card
  DROP COLUMN target_cost,
  DROP COLUMN target_retail_price,
  DROP COLUMN currency,
  DROP COLUMN description,
  DROP COLUMN silhouette,
  DROP COLUMN collar,
  DROP COLUMN fastening,
  DROP COLUMN pockets,
  DROP COLUMN sleeve_cuff,
  DROP COLUMN extra_details,
  DROP COLUMN topstitching,
  DROP COLUMN aux_materials;

-- 12. Sign-off: drop the POM section value (POM removed). Remove any POM rows first, then
--     narrow the section CHECK (tech_card_signoff_chk_1, the first CHECK on the table in 0074).
DELETE FROM tech_card_signoff WHERE section = 'pom';
ALTER TABLE tech_card_signoff
  DROP CHECK tech_card_signoff_chk_1,
  ADD CONSTRAINT chk_tech_card_signoff_section
    CHECK (section REGEXP '^(design|construction|materials|colour|labels|packaging|costing)$');

-- +migrate Down
-- Structural rollback only (the Up is a destructive clean break — dropped POM / matrix /
-- per-size consumption / construction-description data cannot be restored).

ALTER TABLE tech_card_signoff
  DROP CHECK chk_tech_card_signoff_section,
  ADD CONSTRAINT tech_card_signoff_chk_1
    CHECK (section REGEXP '^(design|construction|pom|materials|colour|labels|packaging|costing)$');

ALTER TABLE tech_card
  ADD COLUMN target_cost DECIMAL(10, 2) NULL CHECK (target_cost IS NULL OR target_cost >= 0),
  ADD COLUMN target_retail_price DECIMAL(10, 2) NULL CHECK (target_retail_price IS NULL OR target_retail_price >= 0),
  ADD COLUMN currency VARCHAR(3) NULL,
  ADD COLUMN description TEXT NULL,
  ADD COLUMN silhouette TEXT NULL,
  ADD COLUMN collar TEXT NULL,
  ADD COLUMN fastening TEXT NULL,
  ADD COLUMN pockets TEXT NULL,
  ADD COLUMN sleeve_cuff TEXT NULL,
  ADD COLUMN extra_details TEXT NULL,
  ADD COLUMN topstitching TEXT NULL,
  ADD COLUMN aux_materials TEXT NULL;

ALTER TABLE tech_card_construction
  ADD COLUMN labour_rate DECIMAL(10, 4) NULL CHECK (labour_rate IS NULL OR labour_rate >= 0),
  ADD COLUMN labour_rate_currency VARCHAR(3) NULL;

ALTER TABLE tech_card_bom_item
  ADD COLUMN placement VARCHAR(255) NULL,
  ADD COLUMN consumption DECIMAL(10, 3) NULL CHECK (consumption IS NULL OR consumption >= 0),
  ADD COLUMN quantity DECIMAL(10, 3) NULL CHECK (quantity IS NULL OR quantity >= 0);

CREATE TABLE tech_card_pom_point (
  id INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  section VARCHAR(255) NULL,
  code VARCHAR(32) NULL,
  name VARCHAR(255) NOT NULL,
  how_to_measure TEXT NULL,
  base_value DECIMAL(8, 2) NULL CHECK (base_value IS NULL OR base_value >= 0),
  tolerance_plus DECIMAL(8, 2) NULL CHECK (tolerance_plus IS NULL OR tolerance_plus >= 0),
  tolerance_minus DECIMAL(8, 2) NULL CHECK (tolerance_minus IS NULL OR tolerance_minus >= 0),
  display_order INT NOT NULL DEFAULT 0,
  INDEX idx_tech_card_pom_card (tech_card_id),
  UNIQUE KEY uniq_tech_card_pom_code (tech_card_id, code),
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
);
CREATE TABLE tech_card_pom_grade (
  id INT PRIMARY KEY AUTO_INCREMENT,
  pom_point_id INT NOT NULL,
  size_id INT NOT NULL,
  value DECIMAL(8, 2) NOT NULL CHECK (value >= 0),
  UNIQUE KEY uniq_tech_card_pom_grade (pom_point_id, size_id),
  INDEX idx_tech_card_pom_grade_size (size_id),
  FOREIGN KEY (pom_point_id) REFERENCES tech_card_pom_point(id) ON DELETE CASCADE,
  FOREIGN KEY (size_id) REFERENCES size(id)
);
CREATE TABLE tech_card_pom_actual (
  id INT PRIMARY KEY AUTO_INCREMENT,
  pom_point_id INT NOT NULL,
  size_id INT NULL,
  fitting_id INT NULL,
  label VARCHAR(64) NULL,
  value DECIMAL(8, 2) NOT NULL CHECK (value >= 0),
  display_order INT NOT NULL DEFAULT 0,
  INDEX idx_tech_card_pom_actual_fitting (fitting_id),
  INDEX idx_tech_card_pom_actual_size (size_id),
  FOREIGN KEY (pom_point_id) REFERENCES tech_card_pom_point(id) ON DELETE CASCADE,
  FOREIGN KEY (fitting_id) REFERENCES fitting(id) ON DELETE SET NULL,
  FOREIGN KEY (size_id) REFERENCES size(id)
);
CREATE TABLE tech_card_bom_colorway (
  id INT PRIMARY KEY AUTO_INCREMENT,
  bom_item_id INT NOT NULL,
  colorway_id INT NOT NULL,
  color VARCHAR(255) NULL,
  pantone VARCHAR(64) NULL,
  display_order INT NOT NULL DEFAULT 0,
  UNIQUE KEY uniq_tech_card_bom_colorway (bom_item_id, colorway_id),
  INDEX idx_tech_card_bom_colorway_colorway (colorway_id),
  FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE CASCADE,
  FOREIGN KEY (colorway_id) REFERENCES tech_card_colorway(id) ON DELETE CASCADE
);
CREATE TABLE tech_card_bom_consumption (
  id INT PRIMARY KEY AUTO_INCREMENT,
  bom_item_id INT NOT NULL,
  size_id INT NOT NULL,
  consumption DECIMAL(10, 3) NOT NULL CHECK (consumption >= 0),
  display_order INT NOT NULL DEFAULT 0,
  UNIQUE KEY uniq_tech_card_bom_consumption (bom_item_id, size_id),
  FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE CASCADE,
  FOREIGN KEY (size_id) REFERENCES size(id)
);

ALTER TABLE tech_card_operation DROP COLUMN placement;
DROP TABLE IF EXISTS tech_card_detail_media;
DROP TABLE IF EXISTS tech_card_detail;
DROP TABLE IF EXISTS tech_card_colorway_usage_consumption;
DROP TABLE IF EXISTS tech_card_colorway_usage;

-- +migrate Up
-- Tech card materials (Phase 2): bill of materials (BOM), colorways, and the
-- graded measurement chart (POM). Builds on 0067 (tech_card core).
--
-- Design notes:
--  * Colorway is a first-class entity (Sheet «Колористика»), not a reuse of
--    tech_card_product. tech_card_product stays the set of PUBLISHED catalog SKUs;
--    a colorway is a development colourway with its own lab-dip lifecycle and an
--    OPTIONAL link to the product that realises it once published.
--  * Per-material colour per colourway lives in tech_card_bom_colorway (the matrix
--    cell), so "BOM colour codes hang off the colourway".
--  * POM grade rows key on size_id (the stable size dictionary id), NOT on
--    tech_card_size.id: UpdateTechCard full-replaces child rows every save, so the
--    junction surrogate id is unstable and would orphan the grade.
--  * Money/cost is per BOM line with its own currency; the header target_cost /
--    target_retail_price stay TARGET-only (no rollup stored here).
--  * POM "actual" measurements optionally reference a fitting(id): fit history
--    itself stays in the fitting feature (migration 0064).

-- tech_card_colorway: a development colourway (Sheet «Колористика» columns).
CREATE TABLE tech_card_colorway (
  id INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  code VARCHAR(64) NULL COMMENT 'short colourway code, e.g. BLK',
  name VARCHAR(255) NOT NULL COMMENT 'colourway name (название колорвея)',
  lab_dip_status VARCHAR(16) NOT NULL DEFAULT 'pending'
    COMMENT 'pending|submitted|approved|rejected'
    CHECK (lab_dip_status REGEXP '^(pending|submitted|approved|rejected)$'),
  product_id INT NULL COMMENT 'FK product(id); published SKU realising this colourway',
  comment TEXT NULL,
  display_order INT NOT NULL DEFAULT 0,
  UNIQUE KEY uniq_tech_card_colorway_code (tech_card_id, code),
  INDEX idx_tech_card_colorway_card (tech_card_id),
  INDEX idx_tech_card_colorway_product (product_id),
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
  FOREIGN KEY (product_id) REFERENCES product(id) ON DELETE SET NULL
) COMMENT 'Development colourways for a tech card';

-- tech_card_bom_item: one line of the bill of materials (Sheet «Спецификация»).
CREATE TABLE tech_card_bom_item (
  id INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  section VARCHAR(24) NOT NULL
    COMMENT 'fabric|lining|interlining|insulation|hardware|thread|label|packaging'
    CHECK (section REGEXP '^(fabric|lining|interlining|insulation|hardware|thread|label|packaging)$'),
  name VARCHAR(255) NOT NULL COMMENT 'material name (Наименование материала)',
  placement VARCHAR(255) NULL COMMENT 'Размещение / назначение',
  supplier VARCHAR(255) NULL,
  supplier_ref VARCHAR(255) NULL COMMENT 'Артикул поставщика',
  color VARCHAR(255) NULL COMMENT 'base/default colour (Цвет / колор)',
  composition VARCHAR(255) NULL COMMENT 'Состав',
  spec VARCHAR(255) NULL COMMENT 'Ширина / плотность',
  consumption DECIMAL(10, 3) NULL COMMENT 'Норма расхода'
    CHECK (consumption IS NULL OR consumption >= 0),
  unit VARCHAR(32) NULL COMMENT 'Ед.',
  quantity DECIMAL(10, 3) NULL COMMENT 'Кол-во'
    CHECK (quantity IS NULL OR quantity >= 0),
  unit_price DECIMAL(12, 4) NULL COMMENT 'Цена/ед.'
    CHECK (unit_price IS NULL OR unit_price >= 0),
  currency VARCHAR(3) NULL COMMENT 'ISO 4217 for unit_price',
  comment TEXT NULL COMMENT 'Комментарий (лицо/изнанка)',
  display_order INT NOT NULL DEFAULT 0,
  INDEX idx_tech_card_bom_card (tech_card_id),
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) COMMENT 'Bill of materials lines for a tech card';

-- tech_card_bom_colorway: matrix cell — colour of a BOM material in a colourway.
CREATE TABLE tech_card_bom_colorway (
  id INT PRIMARY KEY AUTO_INCREMENT,
  bom_item_id INT NOT NULL,
  colorway_id INT NOT NULL,
  color VARCHAR(255) NULL COMMENT 'colour name / code for this material in this colourway',
  pantone VARCHAR(64) NULL COMMENT 'Pantone / code',
  display_order INT NOT NULL DEFAULT 0,
  UNIQUE KEY uniq_tech_card_bom_colorway (bom_item_id, colorway_id),
  INDEX idx_tech_card_bom_colorway_colorway (colorway_id),
  FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE CASCADE,
  FOREIGN KEY (colorway_id) REFERENCES tech_card_colorway(id) ON DELETE CASCADE
) COMMENT 'Per-colourway colour of a BOM material (Sheet «Колористика» cells)';

-- tech_card_pom_point: a point of measure (Sheet «Измерения (POM)» rows).
CREATE TABLE tech_card_pom_point (
  id INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  section VARCHAR(255) NULL COMMENT 'group label, e.g. ВЕРХ / КОРПУС',
  code VARCHAR(32) NULL COMMENT 'POM code (Код)',
  name VARCHAR(255) NOT NULL COMMENT 'measurement point (Точка измерения)',
  how_to_measure TEXT NULL COMMENT 'HTM (как измерять)',
  base_value DECIMAL(8, 2) NULL COMMENT 'base-sample value (Базовый образец)'
    CHECK (base_value IS NULL OR base_value >= 0),
  tolerance_plus DECIMAL(8, 2) NULL COMMENT 'tolerance + (Допуск +)'
    CHECK (tolerance_plus IS NULL OR tolerance_plus >= 0),
  tolerance_minus DECIMAL(8, 2) NULL COMMENT 'tolerance - (Допуск -)'
    CHECK (tolerance_minus IS NULL OR tolerance_minus >= 0),
  display_order INT NOT NULL DEFAULT 0,
  INDEX idx_tech_card_pom_card (tech_card_id),
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) COMMENT 'Points of measure for a tech card; values in tech_card.measurement_unit';

-- tech_card_pom_grade: graded value of a POM point for a size in the size range.
-- Keyed on size_id (stable dictionary id), never tech_card_size.id.
CREATE TABLE tech_card_pom_grade (
  id INT PRIMARY KEY AUTO_INCREMENT,
  pom_point_id INT NOT NULL,
  size_id INT NOT NULL COMMENT 'FK size(id)',
  value DECIMAL(8, 2) NOT NULL CHECK (value >= 0),
  UNIQUE KEY uniq_tech_card_pom_grade (pom_point_id, size_id),
  INDEX idx_tech_card_pom_grade_size (size_id),
  FOREIGN KEY (pom_point_id) REFERENCES tech_card_pom_point(id) ON DELETE CASCADE,
  FOREIGN KEY (size_id) REFERENCES size(id)
) COMMENT 'Graded POM values per size';

-- tech_card_pom_actual: an actual measured value (Факт примерка), optionally
-- linked to the fitting session it was taken in.
CREATE TABLE tech_card_pom_actual (
  id INT PRIMARY KEY AUTO_INCREMENT,
  pom_point_id INT NOT NULL,
  fitting_id INT NULL COMMENT 'FK fitting(id); fit session this measurement came from',
  label VARCHAR(64) NULL COMMENT 'free label when no fitting linked, e.g. примерка 1',
  value DECIMAL(8, 2) NOT NULL CHECK (value >= 0),
  display_order INT NOT NULL DEFAULT 0,
  INDEX idx_tech_card_pom_actual_fitting (fitting_id),
  FOREIGN KEY (pom_point_id) REFERENCES tech_card_pom_point(id) ON DELETE CASCADE,
  FOREIGN KEY (fitting_id) REFERENCES fitting(id) ON DELETE SET NULL
) COMMENT 'Actual measured POM values from fittings';

-- +migrate Down
DROP TABLE IF EXISTS tech_card_pom_actual;
DROP TABLE IF EXISTS tech_card_pom_grade;
DROP TABLE IF EXISTS tech_card_pom_point;
DROP TABLE IF EXISTS tech_card_bom_colorway;
DROP TABLE IF EXISTS tech_card_bom_item;
DROP TABLE IF EXISTS tech_card_colorway;

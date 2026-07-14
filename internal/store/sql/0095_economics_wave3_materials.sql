-- +migrate Up
-- Economics audit — Wave 3 / task 10: material catalog + price history.
-- Materials existed only as free-text lines on each tech card's BOM (tech_card_bom_item),
-- and UpdateTechCard full-replaces those lines, so a material's price history vanished on every
-- save and the same fabric across five cards was five unrelated copy-paste rows. Add a shared
-- catalog + an append-only price history. BOM lines gain an OPTIONAL material_id link but keep
-- their own snapshot fields, so a tech card stays a self-contained document that does not drift
-- when the catalog is edited. Free-text (unlinked) lines keep working — no forced backfill.

CREATE TABLE IF NOT EXISTS material (
  id INT PRIMARY KEY AUTO_INCREMENT,
  name VARCHAR(255) NOT NULL,
  section VARCHAR(24) NOT NULL
    COMMENT 'same enum as tech_card_bom_item.section'
    CHECK (section REGEXP '^(fabric|lining|interlining|insulation|hardware|thread|label|packaging)$'),
  supplier VARCHAR(255) NULL,
  supplier_ref VARCHAR(255) NULL COMMENT 'Артикул поставщика',
  composition VARCHAR(255) NULL COMMENT 'Состав',
  spec VARCHAR(255) NULL COMMENT 'Ширина / плотность',
  unit VARCHAR(32) NULL COMMENT 'Ед.',
  fabric_width DECIMAL(7, 2) NULL COMMENT 'usable fabric width, cm'
    CHECK (fabric_width IS NULL OR fabric_width >= 0),
  fabric_weight_gsm DECIMAL(7, 2) NULL COMMENT 'fabric weight, g/m²'
    CHECK (fabric_weight_gsm IS NULL OR fabric_weight_gsm >= 0),
  archived BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_material_section (section),
  INDEX idx_material_archived (archived)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Shared material catalog for tech-card BOM lines';

CREATE TABLE IF NOT EXISTS material_price (
  material_id INT NOT NULL,
  price DECIMAL(12, 4) NOT NULL CHECK (price >= 0),
  currency CHAR(3) NOT NULL COMMENT 'ISO 4217, uppercase (fold to base via costing_fx_rate)',
  valid_from DATE NOT NULL COMMENT 'the price applies from this date; the latest <= today is current',
  source VARCHAR(32) NOT NULL DEFAULT 'manual' COMMENT 'manual | production_run',
  note VARCHAR(255) NULL,
  PRIMARY KEY (material_id, valid_from, currency),
  FOREIGN KEY (material_id) REFERENCES material(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Append-only price history per material';

ALTER TABLE tech_card_bom_item
  ADD COLUMN material_id INT NULL
    COMMENT 'optional link to material catalog; the BOM line keeps its own snapshot fields',
  ADD CONSTRAINT fk_bom_item_material FOREIGN KEY (material_id) REFERENCES material(id) ON DELETE SET NULL;

-- +migrate Down
ALTER TABLE tech_card_bom_item
  DROP FOREIGN KEY fk_bom_item_material,
  DROP COLUMN material_id;

DROP TABLE IF EXISTS material_price;
DROP TABLE IF EXISTS material;

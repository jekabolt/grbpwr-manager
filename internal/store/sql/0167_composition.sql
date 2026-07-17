-- +migrate Up
-- PLM-rework WS3 / S17: structural fibre composition in three layers, replacing the free-text
-- tech_card.composition JSON that was full-replaced-to-NULL from any colourway edit and never derived
-- from the BOM. A controlled `fiber` dictionary + the material's own composition + the BOM line's
-- snapshot/override + the style's derived-or-manual composition. The style composition is aggregated
-- server-side from the shell-fabric BOM lines (see entity.DeriveStyleComposition); a manual override
-- is never overwritten by the auto derivation.
--
-- Additive only (new tables); tech_card.composition JSON is dropped later (M3, after backfill).
-- Idempotent: every table uses an IF-NOT-EXISTS guard; inline CHECKs drop with the table, not by name.
-- (Migration number 0167 allocated by the orchestrator; 0160-0166 belong to WS1/WS2.)

CREATE TABLE IF NOT EXISTS fiber (
    code VARCHAR(8) PRIMARY KEY,
    name VARCHAR(64) NOT NULL,
    archived_at TIMESTAMP NULL
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Controlled fibre vocabulary (COT/POL/WOL/…), R9';

CREATE TABLE IF NOT EXISTS material_composition (
    id INT PRIMARY KEY AUTO_INCREMENT,
    material_id INT NOT NULL,
    fiber_code VARCHAR(8) NOT NULL,
    percent DECIMAL(5, 2) NOT NULL CHECK (percent >= 0 AND percent <= 100),
    UNIQUE KEY uniq_material_fiber (material_id, fiber_code),
    FOREIGN KEY (material_id) REFERENCES material(id) ON DELETE CASCADE,
    FOREIGN KEY (fiber_code) REFERENCES fiber(code) ON DELETE RESTRICT
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Structural fibre composition of a catalog material (replaces free-text)';

CREATE TABLE IF NOT EXISTS bom_item_composition (
    id INT PRIMARY KEY AUTO_INCREMENT,
    bom_item_id INT NOT NULL,
    fiber_code VARCHAR(8) NOT NULL,
    percent DECIMAL(5, 2) NOT NULL CHECK (percent >= 0 AND percent <= 100),
    source VARCHAR(8) NOT NULL DEFAULT 'catalog' CHECK (source REGEXP '^(catalog|override)$'),
    UNIQUE KEY uniq_bomitem_fiber (bom_item_id, fiber_code),
    FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE CASCADE,
    FOREIGN KEY (fiber_code) REFERENCES fiber(code) ON DELETE RESTRICT
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Snapshot-or-override fibre composition of a BOM line';

CREATE TABLE IF NOT EXISTS style_composition (
    id INT PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT NOT NULL,
    fiber_code VARCHAR(8) NOT NULL,
    percent DECIMAL(5, 2) NOT NULL CHECK (percent >= 0 AND percent <= 100),
    source VARCHAR(8) NOT NULL DEFAULT 'auto' CHECK (source REGEXP '^(auto|manual)$'),
    UNIQUE KEY uniq_style_fiber (tech_card_id, fiber_code),
    FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    FOREIGN KEY (fiber_code) REFERENCES fiber(code) ON DELETE RESTRICT
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Garment fibre composition: auto-derived from shell-fabric BOM lines, or a manual override';

-- +migrate Down
DROP TABLE IF EXISTS style_composition;
DROP TABLE IF EXISTS bom_item_composition;
DROP TABLE IF EXISTS material_composition;
DROP TABLE IF EXISTS fiber;

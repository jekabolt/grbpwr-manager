-- +migrate Up
-- Tech card materials/colour/sizes depth (Phase 3.5c):
--  * Colourway lab-dip lifecycle (round, dates, who decided, reject reason) plus a
--    swatch image and a precise colour standard (pantone + system + hex) — the
--    colourist's audit trail and the factory's match target.
--  * BOM fabric data the cutter needs for a marker: usable width, weight (GSM),
--    direction/nap, and a cutting wastage % (folds into line_total).
--  * Per-size order quantity (size run), not just the grade list.
--  * Sketch media gain captions and the new moodboard/reference/swatch kinds;
--    callouts gain normalised x/y so a number can be placed on the drawing.

ALTER TABLE tech_card_colorway
  ADD COLUMN pantone VARCHAR(64) NULL COMMENT 'headline Pantone code',
  ADD COLUMN pantone_system VARCHAR(8) NULL COMMENT 'TCX|TPX|TPG|C|U (Pantone book)'
    CHECK (pantone_system IS NULL OR pantone_system REGEXP '^(TCX|TPX|TPG|C|U)$'),
  ADD COLUMN hex VARCHAR(7) NULL COMMENT 'screen approximation, #RRGGBB'
    CHECK (hex IS NULL OR hex REGEXP '^#[0-9A-Fa-f]{6}$'),
  ADD COLUMN swatch_media_id INT NULL COMMENT 'FK media(id); approved physical swatch',
  ADD COLUMN lab_dip_round INT NULL COMMENT 'lab-dip submission round (1, 2, …)',
  ADD COLUMN lab_dip_submitted_at DATE NULL,
  ADD COLUMN lab_dip_decided_at DATE NULL,
  ADD COLUMN lab_dip_decided_by VARCHAR(255) NULL,
  ADD COLUMN lab_dip_reject_reason TEXT NULL,
  ADD INDEX idx_tech_card_colorway_swatch (swatch_media_id),
  ADD CONSTRAINT fk_tech_card_colorway_swatch FOREIGN KEY (swatch_media_id) REFERENCES media(id) ON DELETE SET NULL;

ALTER TABLE tech_card_bom_item
  ADD COLUMN fabric_width DECIMAL(7, 2) NULL COMMENT 'usable fabric width, cm'
    CHECK (fabric_width IS NULL OR fabric_width >= 0),
  ADD COLUMN fabric_weight_gsm DECIMAL(7, 2) NULL COMMENT 'fabric weight, g/m²'
    CHECK (fabric_weight_gsm IS NULL OR fabric_weight_gsm >= 0),
  ADD COLUMN fabric_direction VARCHAR(8) NULL COMMENT 'any|one_way|two_way (nap / layout)'
    CHECK (fabric_direction IS NULL OR fabric_direction REGEXP '^(any|one_way|two_way)$'),
  ADD COLUMN wastage_percent DECIMAL(5, 2) NULL COMMENT 'cutting wastage %, folded into line_total'
    CHECK (wastage_percent IS NULL OR (wastage_percent >= 0 AND wastage_percent <= 100));

ALTER TABLE tech_card_size
  ADD COLUMN order_qty INT NULL COMMENT 'production order quantity for this size (size run)'
    CHECK (order_qty IS NULL OR order_qty >= 0);

ALTER TABLE tech_card_media
  ADD COLUMN caption VARCHAR(255) NULL COMMENT 'image caption / view name',
  DROP CHECK tech_card_media_chk_1,
  ADD CONSTRAINT chk_tech_card_media_kind
    CHECK (kind REGEXP '^(front|back|detail|lining|preview|moodboard|reference|swatch)$');

ALTER TABLE tech_card_callout
  ADD COLUMN pos_x DECIMAL(5, 4) NULL COMMENT 'normalised x (0..1) of the marker on its sketch'
    CHECK (pos_x IS NULL OR (pos_x >= 0 AND pos_x <= 1)),
  ADD COLUMN pos_y DECIMAL(5, 4) NULL COMMENT 'normalised y (0..1) of the marker on its sketch'
    CHECK (pos_y IS NULL OR (pos_y >= 0 AND pos_y <= 1));

-- +migrate Down
ALTER TABLE tech_card_callout DROP COLUMN pos_y, DROP COLUMN pos_x;
ALTER TABLE tech_card_media
  DROP CHECK chk_tech_card_media_kind,
  ADD CONSTRAINT tech_card_media_chk_1 CHECK (kind REGEXP '^(front|back|detail|lining|preview)$'),
  DROP COLUMN caption;
ALTER TABLE tech_card_size DROP COLUMN order_qty;
ALTER TABLE tech_card_bom_item
  DROP COLUMN wastage_percent, DROP COLUMN fabric_direction,
  DROP COLUMN fabric_weight_gsm, DROP COLUMN fabric_width;
ALTER TABLE tech_card_colorway
  DROP FOREIGN KEY fk_tech_card_colorway_swatch,
  DROP INDEX idx_tech_card_colorway_swatch,
  DROP COLUMN lab_dip_reject_reason, DROP COLUMN lab_dip_decided_by,
  DROP COLUMN lab_dip_decided_at, DROP COLUMN lab_dip_submitted_at, DROP COLUMN lab_dip_round,
  DROP COLUMN swatch_media_id, DROP COLUMN hex, DROP COLUMN pantone_system, DROP COLUMN pantone;

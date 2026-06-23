-- +migrate Up
-- Tech card operation classification (Phase 3.5d): give each sewing operation the
-- real factory taxonomy and the links a sewer needs to read it.
--  * operation_type: machine / stitch class (lockstitch, overlock, coverstitch, …),
--    replacing the coarse seaming/overlock/decorative split.
--  * bom_item_index: which submitted BOM material the operation consumes (thread /
--    binding / interlining / zipper). 0-based; NULL = none. Stored as the literal
--    index because operations and bom_items are full-replaced together every save,
--    so the index is always consistent with the bom_items in the same payload.
--  * callout_number: the numbered pin on the technical sketch the operation realises.
--  * zone: a display-only band (outer / lining / interlining); construction stays a
--    single ordered list, this is grouping only.
-- Back-compat: existing rows get operation_type = 'unknown', zone = 'unknown', and
-- NULL material / callout links.
ALTER TABLE tech_card_operation
  ADD COLUMN operation_type VARCHAR(16) NOT NULL DEFAULT 'unknown'
    COMMENT 'machine/stitch class: unknown|lockstitch|double_needle|overlock|coverstitch|chainstitch|blindhem|bartack|buttonhole|button_attach|fusing|handwork|other'
    CHECK (operation_type REGEXP '^(unknown|lockstitch|double_needle|overlock|coverstitch|chainstitch|blindhem|bartack|buttonhole|button_attach|fusing|handwork|other)$'),
  ADD COLUMN bom_item_index INT NULL
    COMMENT '0-based index into the submitted bom_items of the material this operation applies; NULL = none'
    CHECK (bom_item_index IS NULL OR bom_item_index >= 0),
  ADD COLUMN callout_number INT NULL
    COMMENT 'links to a tech_card_callout.number (assembly point on the sketch); NULL = none',
  ADD COLUMN zone VARCHAR(16) NOT NULL DEFAULT 'unknown'
    COMMENT 'display-grouping band: unknown|outer|lining|interlining|other'
    CHECK (zone REGEXP '^(unknown|outer|lining|interlining|other)$');

-- Widen the BOM section enum to admit soft trims (бейка / тесьма / резинка / кант /
-- шнур / лента), which are neither hardware fittings nor any existing section. The
-- section CHECK is the first (and only original) CHECK on the table, so MySQL named
-- it tech_card_bom_item_chk_1 (see 0073 for the same drop-by-auto-name idiom).
ALTER TABLE tech_card_bom_item
  DROP CHECK tech_card_bom_item_chk_1,
  ADD CONSTRAINT chk_tech_card_bom_item_section
    CHECK (section REGEXP '^(fabric|lining|interlining|insulation|hardware|thread|label|packaging|trim)$');

-- The brand works in mm: make mm the column default too (the app always writes an
-- explicit unit, so this only documents intent for direct inserts).
ALTER TABLE tech_card ALTER COLUMN measurement_unit SET DEFAULT 'mm';

-- +migrate Down
ALTER TABLE tech_card ALTER COLUMN measurement_unit SET DEFAULT 'cm';
ALTER TABLE tech_card_bom_item
  DROP CHECK chk_tech_card_bom_item_section,
  ADD CONSTRAINT tech_card_bom_item_chk_1
    CHECK (section REGEXP '^(fabric|lining|interlining|insulation|hardware|thread|label|packaging)$');
ALTER TABLE tech_card_operation
  DROP COLUMN zone,
  DROP COLUMN callout_number,
  DROP COLUMN bom_item_index,
  DROP COLUMN operation_type;

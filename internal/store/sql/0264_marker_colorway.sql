-- +migrate Up

-- A раскладка belongs to a COLOURWAY, not just to a BOM slot.
--
-- The geometry of a style is one thing: every colourway cuts the same pieces. What differs is the
-- CLOTH — a colourway pins its own catalog article per slot (tech_card_colorway_usage.material_id,
-- 0221), and an article carries its own roll width and кромка. So the same pieces laid on
-- colourway A's 140 cm article and colourway B's 150 cm article are two different layouts with two
-- different measured lengths.
--
-- tech_card_marker recorded fabric_width_cm but had nowhere to say WHOSE width that was. The
-- consequences were all silent: a second marker on the same (size, slot) either lost the name race
-- or sat beside the first with nothing to tell them apart; «применить маркер» in a colourway's
-- recipe offered layouts measured on another colourway's cloth; and costing multiplied a length
-- taken at the wrong width into a number that looks entirely plausible.
--
-- NULL = the layout is not colourway-specific: legacy markers (everything saved before this
-- column existed) and cards whose colourways all share one article. Such a marker stays offered to
-- every colourway, which is what it always was — this migration adds an attribution, it does not
-- retro-assign one, because the data to assign it correctly does not exist.
--
-- FK to `product`, not to a colourway table: 0151 merged tech_card_colorway into product, so a
-- colourway IS the product row whose style_id is the card (fk_tccu_colorway_product precedent).
--
-- ON DELETE SET NULL matches bom_item_id's reasoning — a marker is a MEASUREMENT, and the BOM/
-- colourway edit path must never fail on account of one. Note what that means: a colourway-specific
-- marker whose colourway is deleted becomes a general marker rather than disappearing. That is the
-- deliberate trade. A colourway here is a product row with a lifecycle_status (archived, not
-- deleted), so the case is rare; and losing a measurement outright is worse than widening who it is
-- offered to, because the width it was measured at travels WITH it in fabric_width_cm and the
-- recipe compares the two before applying.
--
-- The (tech_card_id, size_id, name) uniqueness is deliberately NOT widened to include colorway_id.
-- MySQL allows repeated NULLs inside a UNIQUE key, so adding a nullable column to it would quietly
-- drop the guarantee for exactly the general markers that have it today. The name stays the thing
-- that tells two markers apart — the client prefills the colourway into it.

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_marker'
      AND COLUMN_NAME = 'colorway_id'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_marker ADD COLUMN colorway_id INT NULL COMMENT ''the colourway whose article this layout was measured on; NULL = not colourway-specific'' AFTER bom_item_id, ADD CONSTRAINT fk_tcm_colorway FOREIGN KEY (colorway_id) REFERENCES product(id) ON DELETE SET NULL, ADD INDEX idx_tcm_card_colorway (tech_card_id, colorway_id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_marker'
      AND COLUMN_NAME = 'colorway_id'
);
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card_marker DROP FOREIGN KEY fk_tcm_colorway, DROP INDEX idx_tcm_card_colorway, DROP COLUMN colorway_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

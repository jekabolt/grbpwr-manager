-- +migrate Up
-- Link a fitting to a tech card (the style) and make product_id optional.
--
-- The fit of a garment is colour-independent (same pattern across colourways), so
-- a fitting conceptually belongs to the STYLE (tech card); the specific product
-- (colour/SKU) that was sewn as the fit sample is optional metadata. product_id
-- therefore becomes nullable so a prototype can be fitted before any colour SKU
-- exists. One tech card can have many fittings across different products
-- (colourways): they share tech_card_id and differ by product_id.
--
-- The "at least one of (tech_card_id, product_id) must be set" rule is enforced in
-- the API layer, NOT as a CHECK constraint: tech_card_id uses ON DELETE SET NULL,
-- and a CHECK could otherwise block deleting a tech card that has style-only
-- fittings (the SET NULL would violate the CHECK). Existing fittings keep their
-- product_id, so this migration is safe against existing data.

ALTER TABLE fitting
  ADD COLUMN tech_card_id INT NULL COMMENT 'FK tech_card(id) — the style being fitted (colour-independent)' AFTER id,
  MODIFY COLUMN product_id INT NULL COMMENT 'FK product(id) — the specific colour/SKU sample; NULL = style-level / proto',
  ADD INDEX idx_fitting_tech_card (tech_card_id),
  ADD CONSTRAINT fk_fitting_tech_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE SET NULL;

-- +migrate Down
ALTER TABLE fitting
  DROP FOREIGN KEY fk_fitting_tech_card,
  DROP INDEX idx_fitting_tech_card,
  DROP COLUMN tech_card_id,
  MODIFY COLUMN product_id INT NOT NULL COMMENT 'FK product(id) — the garment tried on';

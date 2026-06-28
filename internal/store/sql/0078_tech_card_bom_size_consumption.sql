-- +migrate Up
-- Per-size fabric consumption for BOM lines. Different sizes consume different amounts of
-- fabric, so a single per-garment `consumption` on tech_card_bom_item understates the size
-- run. tech_card_bom_consumption holds the measured rate per size; the size-run cost folds
-- it against the per-size order quantities (tech_card_size.order_qty). When a line has no
-- per-size rows, the single `consumption` stays the fallback (existing behaviour), so this
-- migration is safe against existing tech cards.

CREATE TABLE tech_card_bom_consumption (
  id INT PRIMARY KEY AUTO_INCREMENT,
  bom_item_id INT NOT NULL,
  size_id INT NOT NULL COMMENT 'FK size(id); one of the tech card size range',
  consumption DECIMAL(10, 3) NOT NULL COMMENT 'per-garment rate at this size'
    CHECK (consumption >= 0),
  display_order INT NOT NULL DEFAULT 0,
  UNIQUE KEY uniq_tech_card_bom_consumption (bom_item_id, size_id),
  FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE CASCADE,
  FOREIGN KEY (size_id) REFERENCES size(id)
) COMMENT 'Per-size consumption (норма расхода) of a BOM material';

-- +migrate Down
DROP TABLE tech_card_bom_consumption;

-- +migrate Up
-- Batch of five API updates:
--   1. task: attach a fitting (примерка) + planned/actual start times.
--   2. fitting_callout: numbered pins on fitting photos (pin + note about the fit).
--   3. tech_card_media: split into moodboard vs technical (category column).
--   4. tech_card_costing: drop the pricing block (markup/wholesale/retail) — the
--      costing sheet is now cost-only (per-unit + per-order), pricing lives on the product.

-- 1) task: fitting deep link + planned start (manual) + actual start (server-stamped
-- on the first move into in_progress). fitting_id follows the existing typed-FK
-- deep-link pattern (ON DELETE SET NULL so deleting a fitting never blocks a card).
ALTER TABLE task
  ADD COLUMN fitting_id INT NULL COMMENT 'FK fitting(id); deep link to a try-on session, NULL = none',
  ADD COLUMN start_date DATETIME NULL COMMENT 'planned start (UTC); manual, NULL = none',
  ADD COLUMN started_at DATETIME NULL COMMENT 'actual start: stamped on first move to in_progress (UTC), NULL = not started',
  ADD INDEX idx_task_fitting (fitting_id),
  ADD CONSTRAINT fk_task_fitting FOREIGN KEY (fitting_id) REFERENCES fitting(id) ON DELETE SET NULL;

-- 2) fitting_callout: a numbered marker pinned to a fitting photo, flagging a fit
-- problem at a point on the image. Simpler than tech_card_callout — a pin + a note,
-- no part/dimensions. Full-replace on fitting update; cascades on fitting delete.
CREATE TABLE fitting_callout (
  id INT PRIMARY KEY AUTO_INCREMENT,
  fitting_id INT NOT NULL,
  callout_number INT NOT NULL DEFAULT 0 COMMENT 'marker number shown on the photo',
  note TEXT NULL COMMENT 'what is wrong with the fit here',
  media_id INT NULL COMMENT 'FK media(id); the fitting photo this callout is pinned to (NULL = unanchored)',
  pos_x DECIMAL(5, 4) NULL COMMENT 'normalised x (0..1) of the marker on its photo'
    CHECK (pos_x IS NULL OR (pos_x >= 0 AND pos_x <= 1)),
  pos_y DECIMAL(5, 4) NULL COMMENT 'normalised y (0..1) of the marker on its photo'
    CHECK (pos_y IS NULL OR (pos_y >= 0 AND pos_y <= 1)),
  display_order INT NOT NULL DEFAULT 0,
  INDEX idx_fitting_callout_fitting (fitting_id),
  FOREIGN KEY (fitting_id) REFERENCES fitting(id) ON DELETE CASCADE,
  FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE SET NULL
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Callouts pinned to fitting photos';

-- 3) tech_card_media: sketch media split into two lists. category says which list an
-- item belongs to; kind stays the within-list sub-classifier. Existing rows are
-- back-filled by kind (moodboard/reference/swatch = inspiration → moodboard; the flat
-- sketch kinds → technical, the default). The inline CHECK here auto-names to
-- tech_card_media_chk_1 (the only pre-existing check is the explicitly-named
-- chk_tech_card_media_kind); the Down drops it by that name.
ALTER TABLE tech_card_media
  ADD COLUMN category VARCHAR(16) NOT NULL DEFAULT 'technical'
    COMMENT 'moodboard|technical — which sketch list the item belongs to'
    CHECK (category REGEXP '^(moodboard|technical)$');
UPDATE tech_card_media SET category = 'moodboard'
  WHERE kind IN ('moodboard', 'reference', 'swatch');

-- 4) tech_card_costing: drop the pricing block. The costing sheet is now cost-only
-- (materials + CMT/hardware/packaging/logistics/overhead + defect%, per unit and per
-- order). Pricing (markup → wholesale/retail) moved to the published product.
-- markup_multiplier/wholesale_price/retail_price were created in 0070 as the 7th/8th/9th
-- inline CHECK columns, so MySQL auto-named their checks tech_card_costing_chk_7/_8/_9;
-- a column referenced by a CHECK cannot be dropped, so drop those checks first (same
-- idiom as 0073/0079).
ALTER TABLE tech_card_costing
  DROP CHECK tech_card_costing_chk_7,
  DROP CHECK tech_card_costing_chk_8,
  DROP CHECK tech_card_costing_chk_9,
  DROP COLUMN markup_multiplier,
  DROP COLUMN wholesale_price,
  DROP COLUMN retail_price;

-- +migrate Down
ALTER TABLE tech_card_costing
  ADD COLUMN markup_multiplier DECIMAL(6, 3) NULL COMMENT 'наценка (×)'
    CHECK (markup_multiplier IS NULL OR markup_multiplier >= 0),
  ADD COLUMN wholesale_price DECIMAL(12, 2) NULL COMMENT 'оптовая цена'
    CHECK (wholesale_price IS NULL OR wholesale_price >= 0),
  ADD COLUMN retail_price DECIMAL(12, 2) NULL COMMENT 'розничная цена'
    CHECK (retail_price IS NULL OR retail_price >= 0);
ALTER TABLE tech_card_media
  DROP CHECK tech_card_media_chk_1,
  DROP COLUMN category;
DROP TABLE IF EXISTS fitting_callout;
ALTER TABLE task
  DROP FOREIGN KEY fk_task_fitting,
  DROP COLUMN fitting_id,
  DROP COLUMN start_date,
  DROP COLUMN started_at;

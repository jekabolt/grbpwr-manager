-- +migrate Up

-- PR6 phase 3 (POM / size chart to the style), step 1 of 2: the catalogue size chart (per size, per
-- named measurement, a value) is invariant across a style's colourways — same pattern, colour is the
-- only axis that varies — so it belongs to the STYLE, not to each colourway (product). Today it lives
-- in size_measurement keyed by product_size (one copy per colourway). This adds a style-level table and
-- backfills it from the representative product. Purely additive: nothing reads it yet, so behaviour is
-- unchanged; step 2 flips product's read/write to it and drops size_measurement.
--
-- This is the CATALOGUE size chart (measurement_name dictionary), distinct from the PLM tech-pack POM
-- (tech_card_pom_point/grade/actual) — the two are different representations and are kept separate
-- (same lesson as season code vs the free-text label).
--
-- Representative product = MIN(product.id) per style; the chart is invariant across colourways, so any
-- linked colourway agrees. Idempotent: CREATE TABLE IF NOT EXISTS + INSERT ... ON DUPLICATE KEY UPDATE
-- (a re-run overwrites the same values).

CREATE TABLE IF NOT EXISTS tech_card_size_measurement (
  id                  INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id        INT NOT NULL,
  size_id             INT NOT NULL,
  measurement_name_id INT NOT NULL,
  measurement_value   DECIMAL(10, 2) NOT NULL,
  UNIQUE KEY uniq_tech_card_size_measurement (tech_card_id, size_id, measurement_name_id),
  INDEX idx_tcsm_card (tech_card_id),
  CONSTRAINT fk_tcsm_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
  CONSTRAINT fk_tcsm_size FOREIGN KEY (size_id) REFERENCES size(id) ON DELETE CASCADE,
  CONSTRAINT fk_tcsm_name FOREIGN KEY (measurement_name_id) REFERENCES measurement_name(id) ON DELETE CASCADE
) COMMENT 'Catalogue size chart at the style level (PR6 P3); colourways read it per size';

-- Backfill from the representative product's size_measurement (product_size gives the size_id).
INSERT INTO tech_card_size_measurement (tech_card_id, size_id, measurement_name_id, measurement_value)
SELECT p.style_id, ps.size_id, sm.measurement_name_id, sm.measurement_value
FROM (SELECT style_id, MIN(id) AS pid FROM product GROUP BY style_id) rep
JOIN product p       ON p.id = rep.pid
JOIN size_measurement sm ON sm.product_id = p.id
JOIN product_size ps ON ps.id = sm.product_size_id
ON DUPLICATE KEY UPDATE measurement_value = VALUES(measurement_value);

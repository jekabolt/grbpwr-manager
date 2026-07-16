-- +migrate Up

-- PR6 phase 3 (POM / size chart to the style), step 1 of 2 — REBUILT for contract decision R5.
-- The catalogue size chart (per size, per named measurement, a value) is invariant across a style's
-- colourways, so it belongs to the STYLE (tech_card_size_measurement), not to each colourway's
-- size_measurement. This migrates the legacy per-colourway charts UP to the style.
--
-- The old version copied only MIN(product.id)'s chart and silently discarded every sibling's rows
-- (problem 016). This version RECONCILES all colourways of a style:
--   * equal   — every colourway that defines a (size, measurement) cell agrees on the value -> migrate it;
--   * disjoint/subset — different colourways define different cells -> UNION them;
--   * conflict — two colourways give DIFFERENT values for the SAME cell (after DECIMAL(10,2)
--                normalisation) -> record in a persistent conflict report and STOP (fail-fast guard),
--                exactly like the 0139 style-field reconciliation.
-- Provenance (legacy_measurement_id -> cell) is persisted so the union is auditable/reversible.
--
-- Idempotent: every table is created IF NOT EXISTS; the reports are rebuilt deterministically; the chart
-- insert is ON DUPLICATE KEY UPDATE; every step reads size_measurement, which still exists here (0142
-- drops it later, behind the same guard). The CHECK is named explicitly (never a positional _chk_N).

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

-- 1) Detect conflicts: one (style, size, measurement) cell with >1 distinct normalised value across the
-- style's colourways. This report deliberately survives a failed migration for operator reconciliation.
CREATE TABLE IF NOT EXISTS migration_0141_chart_conflict (
    tech_card_id        INT NOT NULL,
    size_id             INT NOT NULL,
    measurement_name_id INT NOT NULL,
    distinct_values     INT NOT NULL,
    min_value           DECIMAL(10, 2) NOT NULL,
    max_value           DECIMAL(10, 2) NOT NULL,
    detected_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tech_card_id, size_id, measurement_name_id)
);

DELETE FROM migration_0141_chart_conflict;
INSERT INTO migration_0141_chart_conflict
    (tech_card_id, size_id, measurement_name_id, distinct_values, min_value, max_value)
SELECT p.style_id, ps.size_id, sm.measurement_name_id,
       COUNT(DISTINCT ROUND(sm.measurement_value, 2)),
       MIN(ROUND(sm.measurement_value, 2)), MAX(ROUND(sm.measurement_value, 2))
FROM size_measurement sm
JOIN product_size ps ON ps.id = sm.product_size_id
JOIN product p       ON p.id = sm.product_id
GROUP BY p.style_id, ps.size_id, sm.measurement_name_id
HAVING COUNT(DISTINCT ROUND(sm.measurement_value, 2)) > 1;

-- 2) Fail-fast guard: a named CHECK forces MySQL to persist the report and HALTS the migration if any
-- conflict remains (the INSERT violates conflict_count = 0). Contract (0142 drop) re-checks this.
CREATE TABLE IF NOT EXISTS migration_0141_chart_guard (
    singleton      TINYINT NOT NULL PRIMARY KEY,
    conflict_count INT NOT NULL,
    CONSTRAINT migration_0141_no_chart_conflicts CHECK (conflict_count = 0)
);
DELETE FROM migration_0141_chart_guard;
INSERT INTO migration_0141_chart_guard (singleton, conflict_count)
SELECT 1, COUNT(*) FROM migration_0141_chart_conflict;

-- 3) Provenance: which legacy size_measurement row fed which style cell (many legacy rows -> one cell
-- under the union). Auditable and reversible.
CREATE TABLE IF NOT EXISTS migration_0141_chart_provenance (
    legacy_measurement_id INT NOT NULL PRIMARY KEY,
    tech_card_id          INT NOT NULL,
    size_id               INT NOT NULL,
    measurement_name_id   INT NOT NULL
);
INSERT INTO migration_0141_chart_provenance (legacy_measurement_id, tech_card_id, size_id, measurement_name_id)
SELECT sm.id, p.style_id, ps.size_id, sm.measurement_name_id
FROM size_measurement sm
JOIN product_size ps ON ps.id = sm.product_size_id
JOIN product p       ON p.id = sm.product_id
ON DUPLICATE KEY UPDATE tech_card_id = VALUES(tech_card_id),
                        size_id = VALUES(size_id),
                        measurement_name_id = VALUES(measurement_name_id);

-- 4) Union the reconciled chart onto the style. After the guard every remaining cell has exactly one
-- agreed value, so MIN(=MAX) is that value; ROUND normalises to the column's DECIMAL(10,2).
INSERT INTO tech_card_size_measurement (tech_card_id, size_id, measurement_name_id, measurement_value)
SELECT p.style_id, ps.size_id, sm.measurement_name_id, ROUND(MIN(sm.measurement_value), 2)
FROM size_measurement sm
JOIN product_size ps ON ps.id = sm.product_size_id
JOIN product p       ON p.id = sm.product_id
GROUP BY p.style_id, ps.size_id, sm.measurement_name_id
ON DUPLICATE KEY UPDATE measurement_value = VALUES(measurement_value);

-- +migrate Up

-- Widen the marker waste decomposition columns (0261 shipped them as DECIMAL(5,2) capped at 100).
--
-- The two percentages are quoted OF THE PIECE AREA — the same denominator the article's
-- wastage_percent uses, so the numbers stay comparable — and the inter-piece component is
-- 1/efficiency − 1 by construction. That crosses 100% the moment a раскладка lays below 50%
-- efficiency, which awkward small sets on a wide roll genuinely do: the layout then wastes more
-- cloth than it turns into pieces. Under the old bound such a marker made the whole recipe save
-- fail on a CHECK the operator has no way to interpret.
--
-- New ceiling is 1000% (efficiency ~9%), a sanity bound rather than a physical one: past that the
-- input is a mis-entered width, not a marker. The columns are display-only and are never
-- multiplied into a cost, so widening them cannot move any money.

SET @needs := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_colorway_usage'
      AND COLUMN_NAME = 'waste_cut_pct'
      AND NUMERIC_PRECISION = 5
);
SET @ddl := IF(@needs = 1,
    'ALTER TABLE tech_card_colorway_usage DROP CONSTRAINT chk_tccu_waste_selvedge, DROP CONSTRAINT chk_tccu_waste_cut, MODIFY COLUMN waste_selvedge_pct DECIMAL(6,2) NULL, MODIFY COLUMN waste_cut_pct DECIMAL(6,2) NULL, ADD CONSTRAINT chk_tccu_waste_selvedge CHECK (waste_selvedge_pct IS NULL OR (waste_selvedge_pct >= 0 AND waste_selvedge_pct <= 1000)), ADD CONSTRAINT chk_tccu_waste_cut CHECK (waste_cut_pct IS NULL OR (waste_cut_pct >= 0 AND waste_cut_pct <= 1000))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- Values above 100 cannot survive the narrow constraint; clamp them first so the rollback is
-- executable rather than merely declared (the numbers are display-only, so a clamp loses
-- explanation, not money).
UPDATE tech_card_colorway_usage SET waste_selvedge_pct = 100 WHERE waste_selvedge_pct > 100;
UPDATE tech_card_colorway_usage SET waste_cut_pct = 100 WHERE waste_cut_pct > 100;

SET @needs := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_colorway_usage'
      AND COLUMN_NAME = 'waste_cut_pct'
      AND NUMERIC_PRECISION = 6
);
SET @ddl := IF(@needs = 1,
    'ALTER TABLE tech_card_colorway_usage DROP CONSTRAINT chk_tccu_waste_selvedge, DROP CONSTRAINT chk_tccu_waste_cut, MODIFY COLUMN waste_selvedge_pct DECIMAL(5,2) NULL, MODIFY COLUMN waste_cut_pct DECIMAL(5,2) NULL, ADD CONSTRAINT chk_tccu_waste_selvedge CHECK (waste_selvedge_pct IS NULL OR (waste_selvedge_pct >= 0 AND waste_selvedge_pct <= 100)), ADD CONSTRAINT chk_tccu_waste_cut CHECK (waste_cut_pct IS NULL OR (waste_cut_pct >= 0 AND waste_cut_pct <= 100))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

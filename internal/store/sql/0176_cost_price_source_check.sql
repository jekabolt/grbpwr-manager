-- +migrate Up
-- PLM-rework WS8 / Q4 (A1 §10, audit line 76): product.cost_price_source was the last costing
-- provenance field with NO DB validation — it stores where cost_price came from (manual | tech_card
-- | production_run) and drives the seed guard (a manual value must never be silently clobbered by a
-- tech-card/production seed, product.go:722/786/817, stock.go:285), so a bogus value there is a
-- money-provenance bug. Add a named CHECK enumerating the three real sources (NULL = unset, allowed).
-- The app only ever writes code-set values; the pre-normalisation below makes the CHECK safe to add
-- even if some legacy row drifted, so it can never halt a prod boot. Guarded/idempotent on the
-- constraint name.

-- Defensive: null out any value outside the enum before adding the constraint (a bogus provenance
-- string is already meaningless; the cost_price figure itself is untouched). Re-runnable.
UPDATE product
  SET cost_price_source = NULL
  WHERE cost_price_source IS NOT NULL
    AND cost_price_source NOT REGEXP '^(manual|tech_card|production_run)$';

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND CONSTRAINT_NAME = 'chk_product_cost_price_source');
SET @sql := IF(@need,
    'ALTER TABLE product ADD CONSTRAINT chk_product_cost_price_source CHECK (cost_price_source IS NULL OR cost_price_source REGEXP ''^(manual|tech_card|production_run)$'')',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
ALTER TABLE product DROP CONSTRAINT chk_product_cost_price_source;

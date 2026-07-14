-- +migrate Up

-- Make tech_card_packaging weight unit explicit: store integer GRAMS instead of the ambiguous
-- DECIMAL(8,3) weight_net/weight_gross (which held kilograms). The shipping-label weight derivation
-- then reads grams directly, with no kg->g guess. Existing kg values are converted x1000. This is
-- the additive half; migration 0129 drops the old kg columns (kept separate so the riskier
-- column/CHECK drop is isolated, per the small-migrations rule). Grams are INT, so parcels far
-- above 750 g are fine.
--
-- Idempotent: guarded ADD / CHECK / backfill via information_schema (multi-line PREPARE/EXECUTE/
-- DEALLOCATE; a single line trips 1064 on the managed DSN, see 0124).

-- 1) Add integer-grams columns (if missing).
SET @need_cols := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_packaging' AND COLUMN_NAME = 'weight_gross_grams');
SET @sql := IF(@need_cols,
    'ALTER TABLE tech_card_packaging
        ADD COLUMN weight_net_grams   INT NULL DEFAULT NULL,
        ADD COLUMN weight_gross_grams INT NULL DEFAULT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 2) Non-negative CHECKs with explicit stable names (if missing).
SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_packaging'
      AND CONSTRAINT_NAME = 'tcp_weight_gross_grams_nonneg');
SET @sql := IF(@need_chk,
    'ALTER TABLE tech_card_packaging
        ADD CONSTRAINT tcp_weight_net_grams_nonneg   CHECK (weight_net_grams   IS NULL OR weight_net_grams   >= 0),
        ADD CONSTRAINT tcp_weight_gross_grams_nonneg CHECK (weight_gross_grams IS NULL OR weight_gross_grams >= 0)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 3) Backfill grams from the old kg columns while they still exist (0129 drops them). Only rows
-- not yet converted, so a re-run is a no-op.
SET @has_old := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_packaging' AND COLUMN_NAME = 'weight_gross');
SET @sql := IF(@has_old,
    'UPDATE tech_card_packaging
        SET weight_net_grams   = ROUND(weight_net   * 1000),
            weight_gross_grams = ROUND(weight_gross * 1000)
      WHERE weight_net_grams IS NULL AND weight_gross_grams IS NULL
        AND (weight_net IS NOT NULL OR weight_gross IS NOT NULL)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

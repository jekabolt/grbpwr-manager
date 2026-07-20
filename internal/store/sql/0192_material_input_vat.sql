-- +migrate Up
-- Accounting phase 2, wave 1 (VAT engine) — purchase-side input VAT on a material receipt.
-- input_vat_amount is the recoverable VAT of a purchase (base currency EUR), input_vat_regime how it
-- is treated by the M1 posting rule (docs/plan-accounting-phase2/01-wave1-vat.md §1.4):
--   domestic_pl / domestic_uk — Dr 1110 NET + Dr 2080 VAT / Cr 2010 GROSS;
--   wnt / import (Art.33a)    — normal M1 (Dr 1110 / Cr 2010 NET) + self-charge Dr 2080 / Cr 2070.
-- Only receipts carry these; all other movement types leave them NULL.
--
-- Additive + crash-idempotent: MySQL 8 has no ADD COLUMN IF NOT EXISTS, so the add is guarded on
-- information_schema (one atomic ALTER — both columns + the named CHECK, or none). A mid-file failure
-- re-runs from the top. The CHECK is written by hand (a positional *_chk_<n> would drift), and NULL
-- passes it, so movements without input VAT are unaffected.
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement'
      AND COLUMN_NAME = 'input_vat_amount');
SET @sql := IF(@need,
    'ALTER TABLE material_stock_movement
        ADD COLUMN input_vat_amount DECIMAL(12,2) NULL
            COMMENT ''recoverable input VAT of a purchase receipt, base currency (EUR)'',
        ADD COLUMN input_vat_regime VARCHAR(16) NULL
            COMMENT ''input-VAT treatment: wnt|import|domestic_pl|domestic_uk'',
        ADD CONSTRAINT chk_material_input_vat_regime
            CHECK (input_vat_regime IN (''wnt'',''import'',''domestic_pl'',''domestic_uk''))',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
SET @has := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement'
      AND COLUMN_NAME = 'input_vat_amount');
SET @sql := IF(@has,
    'ALTER TABLE material_stock_movement
        DROP CONSTRAINT chk_material_input_vat_regime,
        DROP COLUMN input_vat_regime,
        DROP COLUMN input_vat_amount',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

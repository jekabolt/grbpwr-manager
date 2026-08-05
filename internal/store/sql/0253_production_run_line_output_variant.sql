-- +migrate Up

-- The colour dimension of an AUXILIARY run's plan grid (phase 3 of the colour-variant work; the
-- registry itself is 0252). A run against a variant-mode aux card plans one product-less line PER
-- COLOUR, and at receipt each line's good units are booked into that colour's own warehouse bucket,
-- with its own moving average and its own material_price point, all at the run's single blended
-- unit cost.
--
-- NULL is the overwhelmingly common value and means one of two things, both of which behave exactly
-- as they did before this column existed: a sellable line (it names a product instead), or the
-- single product-less line of a legacy single-output aux card. Zero rows carry a variant until an
-- operator registers colours on a card, so the scalar receipt path stays byte-identical.
--
-- The discriminator is deliberately a NEW column rather than a reuse of product_id. The receipt
-- command aborts an auxiliary receipt the moment ANY line carries a product (a product appearing on
-- an aux run means the plan was edited under the command's read), and that check must keep firing —
-- so the colour identity has to live somewhere product_id does not.
--
-- Idempotent, guarded via information_schema, one statement per line in the PREPARE/EXECUTE/
-- DEALLOCATE trio (a single-line trio trips 1064 on the managed DSN, see 0124 and 0251).

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_line'
      AND COLUMN_NAME = 'output_variant_id');
SET @sql := IF(@need_col,
    'ALTER TABLE production_run_line ADD COLUMN output_variant_id INT NULL COMMENT ''aux colour variant this line produces (NULL = sellable line or legacy single-output aux line)''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- RESTRICT, mirroring the 0252 pins. A colour that a run line references is history, and the store
-- refuses the delete with a readable message before the driver ever gets to raise a 1451; this FK
-- is the backstop for anything that writes around the store.
SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_line'
      AND CONSTRAINT_NAME = 'fk_prl_output_variant');
SET @sql := IF(@need_fk,
    'ALTER TABLE production_run_line ADD CONSTRAINT fk_prl_output_variant FOREIGN KEY (output_variant_id) REFERENCES tech_card_output_variant(id) ON DELETE RESTRICT',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- A line produces EITHER a sellable product or an aux colour, never both. The DTO says the same
-- thing with a readable message; this is the invariant every other writer is held to. Existing rows
-- satisfy it by construction (output_variant_id is NULL everywhere the moment the column lands).
SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_line'
      AND CONSTRAINT_NAME = 'chk_prl_variant_xor');
SET @sql := IF(@need_chk,
    'ALTER TABLE production_run_line ADD CONSTRAINT chk_prl_variant_xor CHECK (output_variant_id IS NULL OR product_id IS NULL)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- Additive and reversible. The order is forced, because the CHECK and the FK both read the column
-- and must go before it does. Each step is guarded so a half-applied Down re-runs cleanly.

SET @has_chk := (SELECT COUNT(*) > 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_line'
      AND CONSTRAINT_NAME = 'chk_prl_variant_xor');
SET @sql := IF(@has_chk,
    'ALTER TABLE production_run_line DROP CHECK chk_prl_variant_xor',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_fk := (SELECT COUNT(*) > 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_line'
      AND CONSTRAINT_NAME = 'fk_prl_output_variant');
SET @sql := IF(@has_fk,
    'ALTER TABLE production_run_line DROP FOREIGN KEY fk_prl_output_variant',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_line'
      AND COLUMN_NAME = 'output_variant_id');
SET @sql := IF(@has_col,
    'ALTER TABLE production_run_line DROP COLUMN output_variant_id',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

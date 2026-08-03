-- +migrate Up

-- production-costing Phase 4 (plan file 05, amendment 7): document facts on a run's manual cost
-- articles. Until now a CMT/logistics/duty article was a bare figure — no supplier, no invoice
-- reference, no VAT split, no payable state — so "деньги потрачено" could never be reconciled
-- against actual bills (the OPEX module has all four; production costs had none). Landed WITH
-- receipt v1 because this phase already rewrites the receive flow that capitalises these articles
-- against AP; Phase 9's vendor reporting then reads them. All nullable: an article remains valid
-- as a bare figure, the document facts are additive.
--
-- supplier(id) is the accounting-wave-4 counterparty table (0201), same FK/ON DELETE SET NULL shape
-- as material_stock_movement.supplier_id. ap_status is the payable lifecycle of the article:
--   accrued  — booked at receive (Dr WIP / Cr AP), no invoice yet;
--   invoiced — the supplier's document arrived (document_ref should be set);
--   paid     — settled; Phase 9 reconciles against bank/OPEX.
SET @need_cols := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_cost' AND COLUMN_NAME = 'supplier_id');
SET @sql := IF(@need_cols,
    'ALTER TABLE production_run_cost
        ADD COLUMN supplier_id INT NULL,
        ADD COLUMN document_ref VARCHAR(128) NULL,
        ADD COLUMN vat_rate DECIMAL(5,2) NULL,
        ADD COLUMN vat_amount DECIMAL(12,2) NULL,
        ADD COLUMN ap_status VARCHAR(16) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_cost' AND CONSTRAINT_NAME = 'fk_prc_supplier');
SET @sql := IF(@have_fk > 0, 'SELECT 1',
    'ALTER TABLE production_run_cost ADD CONSTRAINT fk_prc_supplier FOREIGN KEY (supplier_id) REFERENCES supplier(id) ON DELETE SET NULL');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_cost' AND CONSTRAINT_NAME = 'chk_prc_ap_status');
SET @sql := IF(@have_chk > 0, 'SELECT 1',
    'ALTER TABLE production_run_cost ADD CONSTRAINT chk_prc_ap_status CHECK (ap_status IS NULL OR ap_status REGEXP ''^(accrued|invoiced|paid)$'')');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- Additive nullable columns; dropping them would destroy entered document facts. Intentional no-op.
SELECT 1;

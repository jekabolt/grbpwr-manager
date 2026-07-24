-- +migrate Up
-- Input VAT on OPEX invoices (statutory review 13, P0-1): until now only material receipts posted
-- recoverable VAT (2080); every service invoice (rent, software, professional services, CMT…)
-- booked GROSS into expense and the VAT was silently lost — net VAT payable overstated. These
-- columns record the invoice's VAT split plus the invoice identity the JPK purchase register
-- (ewidencja zakupu) requires: without a document number/date/supplier the deduction cannot form a
-- register row and is excluded from the generated filing (kept in the app summary with a caveat).
--
-- Idempotent: each ADD COLUMN guarded via information_schema (same pattern as 0202); a mid-file
-- crash re-runs the whole file safely. All columns nullable/additive — existing rows unaffected.
SET @sql := IF((SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'opex_line' AND COLUMN_NAME = 'vat_amount') = 0,
    'ALTER TABLE opex_line
        ADD COLUMN vat_amount DECIMAL(12, 2) NULL COMMENT ''input VAT in `currency`; NULL = none recorded'',
        ADD COLUMN vat_amount_base DECIMAL(12, 2) NULL COMMENT ''input VAT folded to base currency; NULL = uncosted or none'',
        ADD COLUMN vat_regime VARCHAR(16) NULL COMMENT ''domestic_pl | domestic_uk'',
        ADD COLUMN doc_number VARCHAR(64) NULL COMMENT ''supplier invoice number (JPK DowodZakupu)'',
        ADD COLUMN doc_date DATE NULL COMMENT ''supplier invoice date (JPK DataZakupu)'',
        ADD COLUMN supplier_vat_id VARCHAR(32) NULL,
        ADD COLUMN supplier_name VARCHAR(255) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @sql := IF((SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'opex_line'
      AND CONSTRAINT_NAME = 'chk_opex_line_vat_regime') = 0,
    'ALTER TABLE opex_line ADD CONSTRAINT chk_opex_line_vat_regime
        CHECK (vat_regime IS NULL OR vat_regime IN (''domestic_pl'', ''domestic_uk''))',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
-- No-op: columns are nullable/additive; dropping them would erase operator-entered invoice data.

-- +migrate Up
-- Depreciation + corporation-tax accrual. Adds two automated posting source types (depreciation,
-- corp_tax) and the fixed-asset register that drives straight-line depreciation. NO new chart-of-
-- accounts rows: depreciation posts to 6370 Depreciation / 1225 Accumulated Depreciation (added by
-- 0195_frs105_coa_accounts), and the CT accrual posts to 8010 Corporation Tax / 2050 Income Tax Payable
-- (added by 0196_accounting_wave3_pnl) — automating the accountant's manual CT journal.

-- Extend chk_acct_entry_source_type (+depreciation, +corp_tax), preserving every current source type
-- (0189 + wave 2/3). Guarded DROP (PREPARE/EXECUTE/DEALLOCATE one-per-line per the idempotency rule)
-- then an unconditional ADD — the DROP clears the constraint first, so the ADD is safe on any re-run.
SET @sql := IF((SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'acct_journal_entry' AND CONSTRAINT_NAME = 'chk_acct_entry_source_type') > 0, 'ALTER TABLE acct_journal_entry DROP CONSTRAINT chk_acct_entry_source_type', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
ALTER TABLE acct_journal_entry ADD CONSTRAINT chk_acct_entry_source_type CHECK (source_type IN ('order_sale','order_refund','order_prepayment','order_transit','order_delivered_sale','material_receipt','material_issue','material_return','material_writeoff','material_adjustment','production_receive','opex_month','shipping_actual','dev_expense','manual','reversal','depreciation','corp_tax'));

-- Fixed-asset register — straight-line depreciation over useful_life_months from acquired_on.
CREATE TABLE IF NOT EXISTS fixed_asset (
    id INT AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    cost_base DECIMAL(12,2) NOT NULL,
    acquired_on DATE NOT NULL,
    useful_life_months INT NOT NULL,
    disposed_on DATE NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_fixed_asset_life CHECK (useful_life_months > 0),
    CONSTRAINT chk_fixed_asset_cost CHECK (cost_base > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down
DROP TABLE IF EXISTS fixed_asset;
SELECT 1;

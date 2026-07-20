-- +migrate Up
-- Depreciation + corporation tax. Adds the two new automated posting source types, the
-- corporation-tax accounts, and the fixed-asset register that drives straight-line depreciation.

-- 1) Allow the new automated source types. DROP CONSTRAINT IF EXISTS + ADD is idempotent and
-- re-runnable after a mid-file failure (the new list is a strict superset of the old — never a
-- data-loss change), and it targets the constraint by its stable explicit name.
ALTER TABLE acct_journal_entry DROP CONSTRAINT IF EXISTS chk_acct_entry_source_type;
ALTER TABLE acct_journal_entry ADD CONSTRAINT chk_acct_entry_source_type CHECK (source_type IN ('order_sale','order_refund','material_receipt','material_issue','material_return','material_writeoff','material_adjustment','production_receive','opex_month','manual','reversal','depreciation','corp_tax'));

-- 2) Corporation-tax accounts: the expense that feeds the FRS 105 tax line, and the payable it accrues
-- into. Additive/idempotent (the 0190 seed pattern).
INSERT INTO acct_account (code, name, section, statement, is_system)
SELECT * FROM (SELECT
    '2075' code, 'Corporation Tax Payable' name, 'liability' section, 'BS' statement, FALSE is_system UNION ALL SELECT
    '6365', 'Corporation Tax', 'opex', 'PL', FALSE
) seed
WHERE NOT EXISTS (SELECT 1 FROM acct_account a WHERE a.code = seed.code);

-- 3) Fixed-asset register — straight-line depreciation over useful_life_months from acquired_on into
-- 1225 Accumulated Depreciation (charge to 6370). cost_base is the base-currency cost.
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
-- Seed accounts and the constraint superset are not reverted (mirrors 0190/0191); the table drop is
-- guarded so a re-run is safe.
DROP TABLE IF EXISTS fixed_asset;
SELECT 1;

-- +migrate Up
-- Chart-of-accounts seeds for the statutory layer (statutory review 13; entity confirmed a UK Ltd
-- with a Polish VAT registration):
--   3005 Called-up Share Capital — FRS 105 requires it as its own SoFP line; the previous chart
--        had only owner's-equity accounts, so a Ltd balance sheet could not present share capital.
--   2045 Payroll Taxes Payable  — withheld PIT + social contributions (ZUS/NI) liability; payroll
--        was a single 6330 lump with no withholding tracking.
--   6335 Employer Social Contributions — the employer-side ZUS/NI cost, split from salaries so the
--        P&L shows true employment cost (opex category `employer_social`).
--   2060 Loans (other)          — referenced by the cash-flow/financing code sets since wave 5 but
--        never seeded; loans other than the 2015 director's loan.
-- Idempotent: INSERT … WHERE NOT EXISTS per code (same pattern as 0195_frs105_coa_accounts).
-- is_system=TRUE: these are statutory/autoposting targets (6335 is the employer_social OPEX
-- accrual account) — archiving one from the UI would make resolveAccounts reject the next
-- automated posting and poison the queue (review pass 2, M-3).
INSERT INTO acct_account (code, name, section, statement, is_system, archived)
SELECT seed.code, seed.name, seed.section, seed.statement, TRUE, FALSE
FROM (
    SELECT '3005' AS code, 'Called-up Share Capital' AS name, 'equity' AS section, 'BS' AS statement
    UNION ALL SELECT '2045', 'Payroll Taxes Payable (PIT/ZUS)', 'liability', 'BS'
    UNION ALL SELECT '6335', 'Employer Social Contributions', 'opex', 'PL'
    UNION ALL SELECT '2060', 'Loans (other)', 'liability', 'BS'
) seed
WHERE NOT EXISTS (SELECT 1 FROM acct_account a WHERE a.code = seed.code);

-- Same protection for the pre-existing autoposting targets seeded is_system=FALSE by
-- 0195_frs105 (1225/6370 — depreciation) and 2015 (director's loan, wizard/report target).
UPDATE acct_account SET is_system = TRUE
WHERE code IN ('1225', '6370', '2015') AND is_system = FALSE;

-- +migrate Down
-- No-op: removing seeded accounts that may already carry journal lines is destructive.

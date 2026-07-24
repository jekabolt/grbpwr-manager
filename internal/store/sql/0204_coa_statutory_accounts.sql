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
INSERT INTO acct_account (code, name, section, statement, is_system, archived)
SELECT seed.code, seed.name, seed.section, seed.statement, FALSE, FALSE
FROM (
    SELECT '3005' AS code, 'Called-up Share Capital' AS name, 'equity' AS section, 'BS' AS statement
    UNION ALL SELECT '2045', 'Payroll Taxes Payable (PIT/ZUS)', 'liability', 'BS'
    UNION ALL SELECT '6335', 'Employer Social Contributions', 'opex', 'PL'
    UNION ALL SELECT '2060', 'Loans (other)', 'liability', 'BS'
) seed
WHERE NOT EXISTS (SELECT 1 FROM acct_account a WHERE a.code = seed.code);

-- +migrate Down
-- No-op: removing seeded accounts that may already carry journal lines is destructive.

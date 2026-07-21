-- +migrate Up
-- FRS 105 (UK micro-entity) chart-of-accounts gap fix. The 0190 seed lacked the accounts a set of
-- FRS 105 accounts needs: depreciation (both the accumulated-depreciation contra-asset that nets 1220
-- Equipment down to net book value on the Statement of Financial Position, and the annual charge on
-- the Income Statement), and a director's / shareholder loan account (a common creditor line, held
-- separately from trade Accounts Payable). Additive and idempotent (INSERT ... WHERE NOT EXISTS, the
-- 0190 seed pattern): a re-run is a no-op, and it never touches an operator-adjusted row.
--
-- 1225 is a contra-asset: it lives in the 'asset' section but carries a credit balance and is
-- subtracted from 1220 to give net book value. Nothing posts to these yet — a depreciation policy /
-- posting is a later step; the accounts exist so FRS 105 can classify them the moment it does.
INSERT INTO acct_account (code, name, section, statement, is_system)
SELECT * FROM (SELECT
    '1225' code, 'Accumulated Depreciation' name, 'asset'     section, 'BS' statement, FALSE is_system UNION ALL SELECT
    '2015', 'Director''s / Shareholder Loan',                'liability', 'BS', FALSE UNION ALL SELECT
    '6370', 'Depreciation',                                 'opex',      'PL', FALSE
) seed
WHERE NOT EXISTS (SELECT 1 FROM acct_account a WHERE a.code = seed.code);

-- +migrate Down
-- Seed accounts are not removed on down (mirrors 0190/0191): once a line references an account,
-- dropping it is unsafe. A no-op keeps the down migration valid.
SELECT 1;

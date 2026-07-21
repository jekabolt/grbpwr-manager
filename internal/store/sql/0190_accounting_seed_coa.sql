-- +migrate Up
INSERT INTO acct_account (code, name, section, statement, is_system)
SELECT * FROM (SELECT
    '1010' code, 'Cash – Bank'                        name, 'asset'     section, 'BS' statement, TRUE  is_system UNION ALL SELECT
    '1030', 'Payment Processor (Stripe)',                   'asset',     'BS', TRUE  UNION ALL SELECT
    '1040', 'Accounts Receivable',                          'asset',     'BS', TRUE  UNION ALL SELECT
    '1110', 'Materials',                                    'asset',     'BS', TRUE  UNION ALL SELECT
    '1120', 'Work in Progress',                             'asset',     'BS', TRUE  UNION ALL SELECT
    '1130', 'Finished Goods',                               'asset',     'BS', TRUE  UNION ALL SELECT
    '1210', 'Prepaid Expenses',                             'asset',     'BS', FALSE UNION ALL SELECT
    '1220', 'Equipment',                                    'asset',     'BS', FALSE UNION ALL SELECT
    '2010', 'Accounts Payable',                             'liability', 'BS', TRUE  UNION ALL SELECT
    '2030', 'Accrued Expenses',                             'liability', 'BS', TRUE  UNION ALL SELECT
    '2070', 'VAT Payable',                                  'liability', 'BS', TRUE  UNION ALL SELECT
    '3010', 'Owner''s Equity',                              'equity',    'BS', FALSE UNION ALL SELECT
    '3020', 'Retained Earnings',                            'equity',    'BS', TRUE  UNION ALL SELECT
    '3030', 'Draws / Distributions',                        'equity',    'BS', FALSE UNION ALL SELECT
    '4010', 'Sales – Retail / Popup',                       'revenue',   'PL', TRUE  UNION ALL SELECT
    '4020', 'Sales – DTC (Website)',                        'revenue',   'PL', TRUE  UNION ALL SELECT
    '4040', 'Returns & Refunds',                            'revenue',   'PL', TRUE  UNION ALL SELECT
    '4110', 'Shipping Income',                              'revenue',   'PL', TRUE  UNION ALL SELECT
    '5010', 'COGS',                                         'cogs',      'PL', TRUE  UNION ALL SELECT
    '5040', 'Inventory Write-offs',                         'cogs',      'PL', TRUE  UNION ALL SELECT
    '5050', 'Returns to Inventory',                         'cogs',      'PL', TRUE  UNION ALL SELECT
    '5090', 'Stock Adjustments',                            'cogs',      'PL', TRUE  UNION ALL SELECT
    '6010', 'Transportation & Office Logistics',            'opex',      'PL', TRUE  UNION ALL SELECT
    '6050', 'Merchant Processing Fees',                     'opex',      'PL', TRUE  UNION ALL SELECT
    '6060', 'Bank Fees',                                    'opex',      'PL', TRUE  UNION ALL SELECT
    '6110', 'Advertising & Marketing',                      'opex',      'PL', TRUE  UNION ALL SELECT
    '6125', 'Production Content',                           'opex',      'PL', TRUE  UNION ALL SELECT
    '6210', 'Samples & Prototyping',                        'cogs',      'PL', TRUE  UNION ALL SELECT
    '6320', 'Software & Subscriptions',                     'opex',      'PL', TRUE  UNION ALL SELECT
    '6330', 'Salaries',                                     'opex',      'PL', TRUE  UNION ALL SELECT
    '6340', 'Rent',                                         'opex',      'PL', TRUE  UNION ALL SELECT
    '6350', 'Professional Services',                        'opex',      'PL', TRUE  UNION ALL SELECT
    '6360', 'Taxes',                                        'opex',      'PL', TRUE  UNION ALL SELECT
    '6390', 'Other Operating Expenses',                     'opex',      'PL', TRUE
) seed
WHERE NOT EXISTS (SELECT 1 FROM acct_account a WHERE a.code = seed.code);

-- +migrate Down
-- seed намеренно не удаляется (down на seed-данных опасен при появившихся ссылках)
SELECT 1;

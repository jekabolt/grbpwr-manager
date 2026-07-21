# Часть 1: схема БД и entity

Две миграции: `0189_accounting_core.sql` (таблицы) и `0190_accounting_seed_coa.sql` (seed плана
счетов). Следующий свободный номер на момент написания — **0189** (максимум сейчас 0188; перед
мерджем перепроверить `ls internal/store/sql | sort | tail`). Правила `migrationlint` обязательны:
идемпотентность (`CREATE TABLE IF NOT EXISTS`, seed через `INSERT ... WHERE NOT EXISTS`),
**именованные** CHECK-констрейнты (никаких автогенерённых `<table>_chk_<n>`), уникальный номер,
маленькие миграции вместо одной большой.

Все «enum'ы» — `VARCHAR` + именованный `CHECK ... IN (...)`, как в `material_stock_movement`
(`chk_msm_type`). Деньги — `DECIMAL(12,2)` (как `opex_line.amount`), суммы строк строго `> 0`
(знак несёт сторона Dr/Cr).

## 0189_accounting_core.sql

```sql
-- +migrate Up

-- Accounting core (double-entry ledger), phase 1. The ledger is a DERIVED, append-only
-- projection of existing operational facts (orders, material movements, production runs,
-- opex) plus manual entries. Base currency EUR. See docs/plan-accounting/.

CREATE TABLE IF NOT EXISTS acct_account (
    id          INT AUTO_INCREMENT PRIMARY KEY,
    code        VARCHAR(8)  NOT NULL,                -- '1030', '4020', ... (Excel-совместимые коды)
    name        VARCHAR(128) NOT NULL,
    -- section управляет местом в отчётах и знаком нормального сальдо:
    -- asset/cogs/opex — дебетовое, liability/equity/revenue — кредитовое.
    section     VARCHAR(16) NOT NULL,
    statement   VARCHAR(2)  NOT NULL,                -- 'BS' | 'PL'
    -- system-счета участвуют в автопостинге: их нельзя архивировать/переименовывать кодом.
    is_system   BOOLEAN NOT NULL DEFAULT FALSE,
    archived    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_acct_account_code (code),
    CONSTRAINT chk_acct_account_section
        CHECK (section IN ('asset','liability','equity','revenue','cogs','opex')),
    CONSTRAINT chk_acct_account_statement CHECK (statement IN ('BS','PL'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS acct_period (
    period      DATE NOT NULL PRIMARY KEY,           -- всегда 1-е число месяца
    status      VARCHAR(8) NOT NULL DEFAULT 'open',
    closed_at   TIMESTAMP NULL,
    closed_by   VARCHAR(255) NULL,                   -- admin username
    CONSTRAINT chk_acct_period_status CHECK (status IN ('open','closed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS acct_journal_entry (
    id           INT AUTO_INCREMENT PRIMARY KEY,
    -- дата хозяйственной операции (не дата постинга); период = первое число её месяца
    occurred_at  DATE NOT NULL,
    description  VARCHAR(512) NOT NULL,
    -- источник + ключ = идемпотентность автопостинга и трассировка к операции
    source_type  VARCHAR(32) NOT NULL,
    source_key   VARCHAR(64) NOT NULL,               -- uuid | uuid:seq | movement id | run id | 'YYYY-MM[:vN]' | manual:<uuid> | rev:<id>
    -- сторно: ссылка на исходную проводку; исходная получает reversed_by (денормализовано для UI)
    reversal_of  INT NULL,
    reversed_by  INT NULL,
    created_by   VARCHAR(255) NOT NULL DEFAULT 'system',
    -- caveat-флаг: в проводке есть допущение (uncosted позиции пропущены и т.п.)
    has_caveat   BOOLEAN NOT NULL DEFAULT FALSE,
    caveat       VARCHAR(512) NULL,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_acct_entry_source (source_type, source_key),
    KEY idx_acct_entry_occurred (occurred_at),
    CONSTRAINT chk_acct_entry_source_type CHECK (source_type IN (
        'order_sale','order_refund',
        'material_receipt','material_issue','material_return',
        'material_writeoff','material_adjustment',
        'production_receive','opex_month','manual','reversal')),
    CONSTRAINT fk_acct_entry_reversal_of FOREIGN KEY (reversal_of)
        REFERENCES acct_journal_entry(id) ON DELETE RESTRICT,
    CONSTRAINT fk_acct_entry_reversed_by FOREIGN KEY (reversed_by)
        REFERENCES acct_journal_entry(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS acct_journal_line (
    id          INT AUTO_INCREMENT PRIMARY KEY,
    entry_id    INT NOT NULL,
    account_id  INT NOT NULL,
    side        VARCHAR(6) NOT NULL,                 -- 'debit' | 'credit'
    amount      DECIMAL(12,2) NOT NULL,              -- base currency (EUR), строго > 0
    -- след исходной валюты для ручных проводок (внесли GBP-аренду → тут 250.00 GBP), NULL для авто
    amount_src   DECIMAL(12,2) NULL,
    currency_src VARCHAR(4) NULL,                    -- VARCHAR(4): 'USDT' (прецедент 0185)
    note        VARCHAR(255) NULL,
    CONSTRAINT chk_acct_line_side CHECK (side IN ('debit','credit')),
    CONSTRAINT chk_acct_line_amount CHECK (amount > 0),
    CONSTRAINT fk_acct_line_entry FOREIGN KEY (entry_id)
        REFERENCES acct_journal_entry(id) ON DELETE CASCADE,
    CONSTRAINT fk_acct_line_account FOREIGN KEY (account_id)
        REFERENCES acct_account(id) ON DELETE RESTRICT,
    KEY idx_acct_line_account (account_id, entry_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Push-outbox для событий заказов (см. 03-event-capture.md). Вставляется в ту же Tx,
-- что и бизнес-операция; разбирается воркером acctposting.
CREATE TABLE IF NOT EXISTS acct_event (
    id           BIGINT AUTO_INCREMENT PRIMARY KEY,
    event_type   VARCHAR(32) NOT NULL,               -- 'order_paid' | 'order_refund'
    source_key   VARCHAR(64) NOT NULL,               -- order uuid | order uuid + ':' + refund seq
    payload      JSON NOT NULL,
    occurred_at  DATETIME NOT NULL,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    processed_at DATETIME NULL,
    attempts     INT NOT NULL DEFAULT 0,
    -- backoff в явном виде: воркер выставляет при неудаче; NULL = можно брать сразу
    next_retry_at DATETIME NULL,
    last_error   VARCHAR(1024) NULL,
    UNIQUE KEY uniq_acct_event (event_type, source_key),
    KEY idx_acct_event_pending (processed_at, next_retry_at, id),
    CONSTRAINT chk_acct_event_type CHECK (event_type IN ('order_paid','order_refund'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Чекпоинты pull-источников (движения склада по id, opex по updated_at и т.п.)
CREATE TABLE IF NOT EXISTS acct_checkpoint (
    source     VARCHAR(32) NOT NULL PRIMARY KEY,     -- 'material_movement' | 'opex_line'
    last_id    BIGINT NULL,
    last_ts    DATETIME NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down

DROP TABLE IF EXISTS acct_checkpoint;
DROP TABLE IF EXISTS acct_event;
DROP TABLE IF EXISTS acct_journal_line;
DROP TABLE IF EXISTS acct_journal_entry;
DROP TABLE IF EXISTS acct_period;
DROP TABLE IF EXISTS acct_account;
```

Замечания по дизайну:

- **`UNIQUE(source_type, source_key)`** — сердце идемпотентности. Для manual-проводок source_key
  генерится в store (`manual:{uuid}`), уникальность сохраняется.
- **Dr=Cr не enforce'ится в DDL** (MySQL не умеет cross-row CHECK) — инвариант держит store
  (`CreateJournalEntry` валидирует баланс до вставки, всё в одной Tx) + сверочный отчёт.
- **`reversal_of`/`reversed_by`** вместо UPDATE/DELETE проводок: закрытым периодам ничего не
  грозит, история полная. RESTRICT на FK — сторно нельзя удалить.
- **Нет FK из `acct_journal_entry` на бизнес-таблицы** (order и т.д.): связь через
  `source_type+source_key`, чтобы не создавать зацепления схем и не мешать чисткам. Тот же
  принцип, что `reference` в `product_stock_change_history`.
- Партиционирования нет — объёмы (тысячи проводок/год) смешные.

## 0190_accounting_seed_coa.sql

Seed плана счетов, Excel-совместимые коды. Идемпотентно:

```sql
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
```

Примечания:

- `6210 Samples & Prototyping` помечен section `cogs`, чтобы в P&L лечь рядом с производственными
  расходами (в Excel он в R&D; если хочется точной секции R&D — добавить section `rnd`, но для
  фазы 1 достаточно существующих шести секций; решаем на ревью).
- Маппинг OPEX-категорий → счета (полный сет — `entity.ValidOpexCategories`,
  `internal/entity/metrics.go:1323`): `salaries→6330`, `rent→6340`, `software→6320`,
  `marketing_other→6110`, `production_content→6125`, `taxes→6360`, `bank_fees→6060`,
  `professional_services→6350`, `logistics_office→6010`, `other→6390`. Маппинг живёт в
  Go-константах (`internal/accounting/accounts.go`), не в БД.
- Кастомные заказы: `payment_method` `cash` → 4010, остальное → 4020 (см. `04-posting-rules.md`).
- Счета 40xx «Less: Discounts» из Excel не сидируем: скидка уже вычтена из `total_settled_base`,
  отдельной проводки скидки в фазе 1 нет (в отчёте P&L можно показать из
  `promo_discount_pct`-аналитики позже — фаза 2).

## Entity: `internal/entity/accounting.go`

Один файл на домен, по образцу `internal/entity/opex.go`. Деньги — `decimal.Decimal` /
`decimal.NullDecimal`, sqlx-теги `db:`.

```go
package entity

type AcctSection string        // asset|liability|equity|revenue|cogs|opex
type AcctSide string           // debit|credit
type AcctSourceType string     // order_sale|order_refund|material_*|production_receive|opex_month|manual|reversal
type AcctEventType string      // order_paid|order_refund

const (
    AcctSideDebit  AcctSide = "debit"
    AcctSideCredit AcctSide = "credit"
    // ... константы для всех enum-значений, как в entity/inventory.go (MaterialMovementType)
)

type AcctAccount struct {
    Id        int          `db:"id"`
    Code      string       `db:"code"`
    Name      string       `db:"name"`
    Section   AcctSection  `db:"section"`
    Statement string       `db:"statement"`
    IsSystem  bool         `db:"is_system"`
    Archived  bool         `db:"archived"`
    CreatedAt time.Time    `db:"created_at"`
    UpdatedAt time.Time    `db:"updated_at"`
}

type AcctJournalLineInsert struct {
    AccountCode string              // резолвится в account_id в store
    Side        AcctSide
    Amount      decimal.Decimal     // EUR, > 0
    AmountSrc   decimal.NullDecimal
    CurrencySrc sql.NullString
    Note        sql.NullString
}

type AcctJournalEntryInsert struct {
    OccurredAt  time.Time
    Description string
    SourceType  AcctSourceType
    SourceKey   string
    CreatedBy   string
    HasCaveat   bool
    Caveat      sql.NullString
    Lines       []AcctJournalLineInsert
}

type AcctJournalEntry struct { /* db-поля entry */ }
type AcctJournalLine struct { /* db-поля line + AccountCode/AccountName из JOIN */ }

type AcctPeriod struct {
    Period   time.Time      `db:"period"`
    Status   string         `db:"status"`
    ClosedAt sql.NullTime   `db:"closed_at"`
    ClosedBy sql.NullString `db:"closed_by"`
}

type AcctEvent struct {
    Id          int64           `db:"id"`
    EventType   AcctEventType   `db:"event_type"`
    SourceKey   string          `db:"source_key"`
    Payload     json.RawMessage `db:"payload"`
    OccurredAt  time.Time       `db:"occurred_at"`
    ProcessedAt sql.NullTime    `db:"processed_at"`
    Attempts    int             `db:"attempts"`
    NextRetryAt sql.NullTime    `db:"next_retry_at"`
    LastError   sql.NullString  `db:"last_error"`
}

// AcctEventInsert — вход EnqueueEvent. Payload — типизированная структура
// (AcctOrderPaidPayload / AcctOrderRefundPayload); JSON-маршалит сам EnqueueEvent,
// чтобы продьюсеры в горячих Tx не возились с json.RawMessage.
type AcctEventInsert struct {
    EventType  AcctEventType
    SourceKey  string
    Payload    any
    OccurredAt time.Time
}

type AcctCheckpoint struct {
    Source    string        `db:"source"`
    LastId    sql.NullInt64 `db:"last_id"`
    LastTs    sql.NullTime  `db:"last_ts"`
    UpdatedAt time.Time     `db:"updated_at"`
}

// Payload'ы событий — типизированные структуры (json-маршалинг в outbox):
type AcctOrderPaidPayload struct {
    OrderUUID string `json:"order_uuid"`
}
type AcctOrderRefundPayload struct {
    OrderUUID      string          `json:"order_uuid"`
    RefundAmount   decimal.Decimal `json:"refund_amount"`    // в валюте заказа, ЭТОГО рефанда, включая доставку
    OrderCurrency  string          `json:"order_currency"`
    RefundedByItem map[int]int64   `json:"refunded_by_item"` // order_item.id → возвращённое qty (для COGS)
}
```

`AcctOrderPaidPayload` намеренно тощий (только uuid): суммы (settled base, fee, vat) воркер
читает из `customer_order` в момент постинга — они могут дозаполниться позже
(`topUpSettledBase`), и мы не хотим замораживать в payload устаревший NULL.
`AcctOrderRefundPayload` наоборот несёт сумму: конкретный рефанд из агрегатной
`refunded_amount` потом не восстановить.

## Правки в migrationlint (проверено по коду линта)

Как линт устроен на самом деле (`internal/store/migrationlint/`):

- `numbering_test` — увидит 0189/0190 автоматически (grandfathered baseline: только дубль 0003).
- `idempotency_test` (`grandfatheredMigrationMax = 92`, наши файлы выше — проверяются)
  контролирует **только** два паттерна: каждый `CREATE TABLE` обязан быть
  `IF NOT EXISTS`, и нет `DROP CHECK <table>_chk_<n>` по auto-имени. Наш SQL проходит.
  **Идемпотентность INSERT-seed'а линт НЕ проверяет** — `WHERE NOT EXISTS` в 0190 держится
  на ревью и на прогоне «дважды на чистой БД» (шаг 1.5 в части 10).
- `enum_drift_test` — это **ручные** пары: по одной test-функции на констрейнт, грепающей
  конкретный файл миграции (`extractDBEnumValues(t, content, "chk_...", ...)`) и сравнивающей
  с entity-сетом. Новые CHECK'и он сам не заметит. Имплементатору добавить три функции по
  образцу существующих (`chk_material_class` ↔ `ValidMaterialClasses`):
  `chk_acct_entry_source_type` ↔ `entity.ValidAcctSourceTypes`,
  `chk_acct_line_side` ↔ `entity.ValidAcctSides`,
  `chk_acct_event_type` ↔ `entity.ValidAcctEventTypes` — и, соответственно, завести эти
  `Valid*`-map'ы в `entity/accounting.go` (по образцу `ValidOpexCategories`).

# Accounting (General Ledger) — план имплементации. Часть 0: обзор

Цель: перенести функциональность Excel-файла `GRB_accouting_system.xlsx` (двойная запись, план
счетов, журнал, P&L, Balance Sheet, производственный костинг, инвентарные балансы) в бекенд
`grbpwr-manager`, встроив её в существующую архитектуру, а не рядом с ней.

## Принятые решения (зафиксированы с владельцем)

| Вопрос | Решение |
|---|---|
| Базовая валюта книг | **EUR** — родная база бекенда (`total_settled_base`, `cost_price`, `material_stock.avg_unit_cost_base` — всё EUR). GBP-конвертация для UK filing — фаза 2, поверх леджера через `costing_fx_rate`. |
| Scope фазы 1 | Ядро: план счетов + журнал двойной записи + автопроводки из существующих событий + ручные проводки + Trial Balance / P&L / Balance Sheet + закрытие периодов + сверка. |
| Вне фазы 1 | VAT-режимы (OSS/WDT/export), крипто-учёт (FRS 105), Cash Flow statement, Financial Health ratios, AP-субледжер поставщиков, банковский CSV-импорт, GBP-репортинг, JPK_VAT/OSS exports. Бэклог — в `08-rollout-testing.md`. |
| Банк/касса | Ручные проводки через admin API. Stripe-часть постится автоматически на счёт 1030 Payment Processor. |
| История | Старт с нуля: леджер пустой, проводки только с cutover-даты (`accounting.start_date`). Opening balance при желании вносится позже обычной ручной проводкой (`source_type = manual`). |

## Что уже есть в бекенде (и что мы переиспользуем, а не дублируем)

Финансовый слой уже наполовину построен — важно **не** строить параллельные истины:

- **Выручка**: `customer_order.total_settled_base` (EUR, факт Stripe-сеттлмента) + `payment_fee`
  (EUR, комиссия Stripe) — пишутся в `Order().UpdateSettledBaseAndFee`
  (`internal/store/order/payment.go:215`). Правило CLAUDE.md «два EUR-показателя» действует и для
  леджера: **выручка постится только из `total_settled_base`**, `total_price_eur` в проводки не
  попадает никогда (loyalty-показатель).
- **VAT-снапшот заказа**: `customer_order.vat_rate_pct` / `vat_amount` (inclusive, в валюте заказа,
  миграция 0094) + справочник `vat_rate` (country_code, rate_pct, valid_from).
- **COGS**: `order_item.cost_price_at_sale` (EUR/ед., снапшот при продаже, 0093) — готовый источник
  проводки Dr COGS / Cr Finished Goods.
- **Материальный склад**: `material_stock` (moving-average EUR) + `material_stock_movement` —
  **уже append-only леджер** с 8 типами движений (`receipt | receipt_production | issue_production |
  issue_sample | return_production | return_sample | adjustment | writeoff`), `unit_cost_base`
  заморожен на строке. Это идеальный pull-источник проводок — код склада менять не нужно.
- **Производство**: `production_run` + `production_run_cost` (kinds `materials|cmt|hardware|packaging|logistics|duty|other`,
  `amount_base`), receive → `product.cost_price` (`ActualUnitCostBase`,
  `internal/store/productionrun/productionrun.go:216,264`). Партия = аналог «Production Batches» из Excel.
- **OPEX**: `opex_line` / `opex_recurring` (0112) + воркер `internal/opexmaterialize`. Это готовый
  журнал операционных расходов — леджер постит из него, вторую форму ввода расходов не создаём.
- **FX**: `costing_fx_rate` (ECB, воркер `internal/fxsync`) — понадобится для фазы 2 (GBP, USDT).
- **Инвентарная оценка**: `Metrics().GetInventoryValuation` (raw/WIP/FG в EUR) — используем для
  сверки леджера со складом, не заменяем.
- **Инфраструктура**: паттерн воркеров (`ordercleanup` и др.), `Tx` через `dependency.Repository`,
  миграции `internal/store/sql` (+ `migrationlint`), mockery, RBAC (`internal/rbac`), деньги =
  `shopspring/decimal` ↔ `google.type.Decimal`.

## Чего нет (и что строим)

1. Плана счетов и журнала двойной записи — вся «бухгалтерия» сейчас размазана мутируемыми
   колонками (`total_settled_base`, `refunded_amount`) и производными метриками.
2. Автопостинга: события (оплата, рефанд, движения склада, receive партии, OPEX) не порождают
   проводок.
3. Ручных проводок (зарплаты, аренда сверх OPEX, банк, займы, equity).
4. Отчётов P&L / Balance Sheet / Trial Balance в терминах счетов.
5. Закрытия периодов и сверки (ledger ↔ операционные данные).

## Архитектура решения (карта частей)

```
                    ┌──────────────────────────────────────────────────────┐
                    │  Существующие события                                │
                    │                                                      │
 push (outbox):     │  Order().OrderPaymentDone (Stripe-оплата)            │──┐
                    │  Order().CreateCustomOrder (cash / bank-invoice)     │──┤ acct_event
                    │  Order().RefundOrder                                 │──┘ (в той же Tx)
                    │                                                      │
 pull (checkpoint): │  material_stock_movement (append-only, по id)        │
                    │  production_run (received_at, по source_key)         │
                    │  opex_line (по updated_at, repost месяца)            │
                    └──────────────────────────────────────────────────────┘
                                          │
                                          ▼
                    internal/acctposting (воркер, паттерн ordercleanup)
                    правила проводок = internal/accounting (чистая логика)
                                          │
                                          ▼
                    acct_journal_entry + acct_journal_line  (append-only, Dr=Cr)
                    acct_account (план счетов)   acct_period (open/closed)
                                          │
                                          ▼
                    internal/store/accounting: Trial Balance, P&L, BS,
                    drill-down по счёту, сверка ledger↔операционка
                                          │
                                          ▼
                    proto AdminService (+RBAC section "accounting") → админка
```

Ключевые принципы:

- **Леджер — производный, но append-only.** Источник истины по операциям — существующие таблицы;
  леджер — их бухгалтерская проекция + ручные проводки. Проводки не редактируются — только сторно
  (reversal), как и `material_stock_movement`.
- **Гибридный захват событий.** Для заказов — push-outbox (точные суммы конкретного рефанда иначе
  не восстановить: `refunded_amount` копится агрегатом). Для склада/производства/OPEX — pull по
  чекпоинту (ноль правок в горячем коде). Обоснование — в `03-event-capture.md`.
- **Идемпотентность везде**: `UNIQUE(source_type, source_key)` на entry; повторная обработка
  события/движения — no-op. Это же даёт бесплатный re-post при багфиксах.
- **Не ломаем горячие пути.** Единственное изменение в платёжном флоу — одна вставка строки
  outbox в уже открытую транзакцию. Постинг падает → падает воркер (backoff, health), не оплата.
- **Uncosted не выдумываем.** Движение без `unit_cost_base`, позиция без `cost_price_at_sale` —
  не постится и попадает в сверочный отчёт (та же философия, что `CoveragePct`/caveat в метриках).
- **Учётная политика явная** (04): признание выручки/COGS в момент подтверждения оплаты;
  P&L фазы 1 — до налога на прибыль и без расходов доставки перевозчику, и оба факта
  печатаются в caveats отчёта, а не подразумеваются.

## Состав частей (одна часть = один файл = примерно один PR)

| Файл | Содержимое | Зависит от |
|---|---|---|
| `01-db-schema.md` | Миграции 0189–0190: `acct_account`, `acct_journal_entry`, `acct_journal_line`, `acct_period`, `acct_event`, `acct_checkpoint`, seed плана счетов. Entity. | — |
| `02-store-layer.md` | Пакет `internal/store/accounting`, интерфейс `dependency.Accounting`, wiring в `store.go`, моки. | 01 |
| `03-event-capture.md` | Outbox-продьюсеры в `internal/store/order` (3 точки: Stripe-оплата, кастомные заказы, рефанд), pull-источники, чекпоинты. | 01, 02 |
| `04-posting-rules.md` | Полная таблица правил Dr/Cr для каждого события: суммы, счета, edge-cases. | — (спека) |
| `05-admin-api.md` | Proto RPC, dto, `internal/apisrv/admin/accounting.go`, RBAC-секция `accounting`. | 02 |
| `06-reports.md` | SQL и контракты Trial Balance, P&L, Balance Sheet, drill-down, сверка. | 02 |
| `07-worker-config.md` | Воркер `internal/acctposting`, секция конфига `accounting`, wiring в `app/app.go`, health. | 02, 03, 04 |
| `08-rollout-testing.md` | Порядок PR, тесты (store integration, migrationlint, RBAC completeness), beta→prod, риски, бэклог фазы 2. | все |
| `09-implementation-notes.md` | Справочник имплементатора: grep-верифицированные факты кода (файлы:строки, сигнатуры), готовый SQL order facts, конвенции, FAQ с принятыми решениями, acceptance criteria по PR, известные ловушки. **Агентам читать первым вместе с 00.** | — |
| `10-implementation-order.md` | Операционный маршрут: граф зависимостей шагов, пошаговый порядок с DoD и коммитами, что можно параллелить между агентами, анти-чеклист срезания углов. **Точка входа для запуска работ.** | все |

## Соответствие листам Excel

| Лист Excel | Реализация |
|---|---|
| Chart of Accounts / _Setup | `acct_account` + seed (01) |
| General Ledger | `acct_journal_entry`/`acct_journal_line` + автопостинг (01, 03, 04) |
| SKU Revenue Calc | не нужен — заказы уже в БД; постинг из `order_paid`/`order_refund` (04) |
| Production Batches | уже есть (`production_run` + `ActualUnitCostBase`); леджер отражает receive (04) |
| Inventory (RM/WIP/FG) | уже есть (`material_stock`, `GetInventoryValuation`); леджер-счета 1110/1120/1130 + сверка (06) |
| Income Statement | `GetProfitLossStatement` (06) |
| Balance Sheet | `GetBalanceSheet` + balance check (06) |
| Cash Flow | фаза 2 (08) |
| Financial Health | частично уже есть в `GetMetrics`; ratios поверх леджера — фаза 2 (08) |
| VAT&Crypto, B2B & Cash Rules | VAT-снапшот постится на 2070; режимы/OSS/крипта — фаза 2 (08) |
| FRS 105 Compliance | фаза 2 (08) |
| Unsorted / Лист1 | ручные проводки + сверочный отчёт «не разнесено» (05, 06) |

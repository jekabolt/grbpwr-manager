# Часть 10: порядок имплементации — пошаговый маршрут

Этот файл — операционный маршрут: что за чем делать, какими шагами, что запускать после
каждого шага и что можно вести параллельно. Части 01–07 описывают «что», этот файл — «в каком
порядке и как убедиться, что шаг завершён».

## Граф зависимостей шагов

Нумерация шагов = нумерация PR в 08 (и acceptance criteria в 09.5).

```
Шаг 1 (schema+entity) ──► Шаг 2 (store) ──► Шаг 4 (producers) ──► Шаг 5 (worker)
        │                     │                                        ▲
        │                     ├──► Шаг 6 (api) ──► Шаг 7 (reports API) │
        └── Шаг 3 (rules, БЕЗ зависимостей — параллелен 2 и 4) ────────┘
                                              Шаг 8 (alerts) — после 5 и 7
```

Параллелить безопасно: **Шаг 3** (чистые билдеры) можно писать одновременно с 2 и 4 — он
зависит только от entity-типов шага 1. **Шаг 6** (API) можно начинать после шага 2, не
дожидаясь 4–5. Всё остальное — строго последовательно. Если работают несколько агентов:
агент A — 2→4→5, агент B — 3, агент C — 6→7 (после мерджа 2).

## Шаг 0 — подготовка (5 минут, вручную или первой командой агента)

1. `git checkout beta && git pull && git checkout -b feature/accounting-core`.
2. Прочитать `CLAUDE.md`, `docs/plan-accounting/00-overview.md`, `09-implementation-notes.md`
   и файл своей части.
3. Проверить актуальность якорей: `ls internal/store/sql | sort | tail -3` (следующий номер
   миграции всё ещё 0189?); `grep -n "func (s \*Store) OrderPaymentDone" internal/store/order/payment.go`;
   `grep -n "func (s \*Store) RefundOrder" internal/store/order/lifecycle.go`. Разъехалось —
   сначала поправить план.
4. Убедиться, что локальная MySQL из `config/config.toml` доступна: `make run-quick` стартует.

## Шаг 1 — схема и entity (часть 01). ~0.5 дня

Порядок внутри шага:

1. `internal/store/sql/0189_accounting_core.sql` — DDL из 01 (6 таблиц).
2. `internal/store/sql/0190_accounting_seed_coa.sql` — seed 34 счетов.
3. `internal/entity/accounting.go` — типы из 01 (включая `AcctEventInsert`, `AcctCheckpoint`,
   payload-структуры, константы enum'ов).
4. `internal/store/migrationlint/enum_drift_test.go`: добавить **три test-функции** по образцу
   существующих (`chk_material_class` ↔ `ValidMaterialClasses`) для
   `chk_acct_entry_source_type` / `chk_acct_line_side` / `chk_acct_event_type` — линт сам
   новые CHECK'и не видит (пары ручные, см. 01). Соответствующие `Valid*`-map'ы — в
   `entity/accounting.go`. Затем `go test ./internal/store/migrationlint/...`.
5. Прогнать миграции дважды на локальной БД (идемпотентность): запуск приложения с
   `MYSQL_AUTOMIGRATE=true` два раза подряд; второй — без ошибок.

DoD: `make build-quick` зелёный; `SELECT COUNT(*) FROM acct_account` = 34; повторный boot чист.
Коммит: `accounting: schema + chart of accounts seed (0189, 0190)`.

## Шаг 2 — store-слой (часть 02). ~1 день

Порядок внутри шага (компилируемость после каждого пункта):

1. `internal/dependency/dependency.go`: интерфейс `Accounting` (полный, из 02, включая
   worker-facing методы) + `Accounting() Accounting` в `Repository`.
2. `make generate` — мокери сгенерит `mock_Accounting.go`, перегенерит `mock_Repository.go`.
   Компиляция сломается на `MYSQLStore` — это ожидаемо, чинится п.3.
3. `internal/store/accounting/` — файлы в порядке: `accounting.go` (Store, New, ошибки,
   CreateJournalEntry, аккаунты) → `periods.go` → `events.go` → `checkpoints.go` →
   `ledger.go` → `orderfacts.go` (worker-facing чтения; отчёты — заглушки
   `return nil, errors.New("not implemented")` до шага 7).
4. Wiring: `internal/store/store.go` — поле, `initSubStores` (:373), `initSubStoresForTx`
   (:402), аксессор.
5. Тесты: `internal/store/accounting_core_integration_test.go` (кейсы из 02) —
   `go test ./internal/store/ -run TestAccounting -v`.

DoD: тесты зелёные, включая «rep.Accounting() внутри Tx не nil». Коммит:
`accounting: store layer (journal, periods, outbox, checkpoints)`.

## Шаг 3 — билдеры проводок (часть 04). ~1 день, параллелен шагам 2 и 4

1. `internal/accounting/accounts.go` — константы кодов + маппинг OPEX-категорий.
2. `ordersale.go` + `ordersale_test.go` (S1 + гварды вырожденных сумм) — table-driven.
3. `refund.go` + тест (S2, каскадные гварды).
4. `material.go` + тест (M1–M8 — таблица из 04 буквально).
5. `production.go` + тест (P1: MANUAL + LEDGER_WIP → FG, caveat при uncosted).
6. `opex.go` + тест (O1, маппинг категорий, «пустой месяц»).
7. Property-тест: рандомные пропорции VAT/SHIP/fee → всегда Σdebit==Σcredit.

Зависимости: только `internal/entity` (типы шага 1) + `shopspring/decimal`. Никакого store.
DoD: `go test ./internal/accounting/... -v` зелёный. Коммит: `accounting: posting rule builders`.

## Шаг 4 — продьюсеры событий (часть 03). ~0.5 дня, самый ревьюируемый шаг

Три точечных диффа + тесты. Делать по одному, с тестом после каждого:

1. `OrderPaymentDone` (`internal/store/order/payment.go:476`): после `wasUpdated = true` —
   `EnqueueEvent(order_paid)`. Тест: существующий интеграционный флоу оплаты + проверка
   строки в `acct_event`; повторный вызов → одна строка.
2. `CreateCustomOrder` (`internal/store/order/create.go:127`): обернуть финальный
   `return insertOrderStatusHistoryEntry(...)` + `EnqueueEvent(order_paid)`. Тест: cash-заказ
   → событие.
3. `RefundOrder` (`internal/store/order/lifecycle.go:235`): seq-подсчёт + событие с
   `refundedAmount`/`refundedByItem`. Тест: полный и частичный рефанд → два события с
   разными source_key и точными суммами; роллбек Tx → нет события.

Правила ревью шага: диффы только добавляют вставку в конец существующих Tx-замыканий; никакого
изменения существующей логики; ошибки пробрасываются. DoD: все существующие order-тесты
зелёные + новые. Коммит: `accounting: outbox producers in order flows`.

## Шаг 5 — воркер (часть 07). ~1 день. После 2+3+4

1. `internal/acctposting/acctposting.go` — скопировать каркас с `internal/ordercleanup`
   (Config/Worker/New/Start/Stop/Name/LastSuccess), поля конфига из 07.
2. `worker.go` — цикл (тоже с ordercleanup, включая backoff и `saferun.Recover`).
3. `outbox.go` — обработка order_paid/order_refund. **Правило локов из 07**: факты на пуле,
   запись короткой Tx. Ветки готовности: settled / не-Stripe EUR / не-Stripe не-EUR (skip) /
   ожидание.
4. `pull.go` — фазы movements / production receives / opex.
5. Конфиг: `config/cfg.go` (поле + bindEnvVars + Validate), `.do/app-beta.yaml`.
6. `app/app.go`: поле `ap`, старт (gated Enabled, после `cache.SetDefaultCurrency`), Stop-
   цепочка, `buildHealthRegistry` → `addWorker(a.ap)`.
7. `internal/store/acctposting_integration_test.go` — сценарии из 07 (включая «повторный
   runOnce без дублей» и «settled дозаполнился — событие допостилось»).

DoD: локально с `ACCOUNTING_ENABLED=true` тестовый флоу (заказ→оплата→рефанд, приход
материала, receive руна, opex) порождает корректные проводки; `/statusz` показывает воркер.
Коммит: `accounting: posting worker + config + app wiring`.

## Шаг 6 — API без отчётов (часть 05). ~1 день. После 2, параллелен 3–5

1. `proto/admin/admin/admin.proto`: RPC + messages (без отчётных — их в шаге 7; или все
   сразу, но хендлеры отчётов заглушками Unimplemented). Литеральные пути после `/{id}`.
2. `make proto` → `make internal/statics`.
3. `internal/dto/accounting.go` — конвертеры + валидация.
4. `internal/apisrv/admin/accounting.go` — хендлеры (CRUD счетов, journal, периоды).
5. `internal/rbac/rbac.go`: секция в `catalog` (:79) + все RPC в `methodRequirements` (:137).
   `go test ./internal/rbac/...` — completeness-тест зелёный.
6. `make check-proto-contracts` — аддитивность.

DoD: через Swagger UI (корень `/`) руками: создать ручную проводку, увидеть её в списке,
сторнировать; несбалансированная → InvalidArgument. Коммит: `accounting: admin API (accounts,
journal, periods) + RBAC`.

## Шаг 7 — отчёты (часть 06). ~1 день. После 6 (API-каркас) и 2

1. `internal/store/accounting/reports.go` — CTE + TrialBalance → ProfitLoss → BalanceSheet →
   AccountLedger (в этом порядке: каждый следующий переиспользует хелперы предыдущего).
2. `reconcile.go` — блоки сверки (revenue/fees/COGS/1110/1130/pending/unposted).
3. Достроить proto/dto/хендлеры отчётов (снять заглушки).
4. `internal/store/accounting_reports_integration_test.go` — сквозной пример из 04 числами.

DoD: тест сквозного примера зелёный (TB balanced, P&L 203.25/84.50/7.55, BS CHK=0);
`GetAcctReconciliation` на тестовых данных даёт нулевые дельты. Коммит: `accounting: reports
(TB, P&L, BS, ledger drill-down, reconciliation)`.

## Шаг 8 — алерты (часть 07, финал). ~0.5 дня. После 5 и 7

1. `internal/store/metrics/dashboard.go`: коды `acct_posting_lag`,
   `acct_manual_entry_required`, `acct_reconciliation_drift` в `buildDashboardAlerts`
   (данные — из готовых запросов `reconcile.go` через `repo.Accounting()`).
2. При желании — поле порога в `AlertThresholds` (+ dto/proto/UI-поле, по образцу
   `production_run_stale_days`).

DoD: тест с искусственно зависшим событием → алерт в `GetDashboard`. Коммит:
`accounting: dashboard alerts`.

## Шаг 9 — beta rollout (часть 08)

1. Merge feature-ветки в `beta` → авто-деплой; миграции применятся сами.
2. `.do`-энвы беты: `ACCOUNTING_ENABLED=true`, `ACCOUNTING_START_DATE=<сегодня>`.
3. Smoke по чек-листу 08 (тестовая карта → S1; рефанд → S2; приход → M1; receive → P1;
   ручная проводка; TB/сверка).
4. Неделя soak → merge в `master` с `ACCOUNTING_ENABLED=false` → проверить накопление
   событий → включить env на проде с прод-датой cutover.

## Анти-чеклист (типовые срезания углов — запрещены)

- НЕ постить синхронно из бизнес-Tx «раз уж мы всё равно там» — только outbox/pull.
- НЕ читать исходные таблицы внутри worker-Tx (SERIALIZABLE-локи — 07/09).
- НЕ использовать `total_price_eur` в постинге (CLAUDE.md; только settled / total_price
  для не-Stripe EUR).
- НЕ добавлять FK из acct_-таблиц на бизнес-таблицы.
- НЕ редактировать/удалять проводки — только сторно.
- НЕ пропускать tx-ветку wiring (`initSubStoresForTx`) — nil-паника всплывёт позже.
- НЕ мапить новые RPC «потом» — rbac-тест должен падать до тех пор, и это правильно.
- НЕ округлять промежуточные доли — только финальные строки (04).
- НЕ двигать baseline'ы `proto/contracts` — изменения аддитивны по построению.

## Ожидаемый суммарный объём

~9 рабочих шагов, 6–8 дней одним исполнителем или ~4 дня тремя параллельными агентами
(A: 2→4→5, B: 3, C: 6→7 после мерджа 2; шаги 1 и 8–9 — последовательные точки синхронизации).

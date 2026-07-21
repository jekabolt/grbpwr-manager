# Часть 2: store-слой (`internal/store/accounting`) и dependency-интерфейс

Новый саб-стор по образцу `internal/store/inventory` / `internal/store/metrics`: пакет со
структурой `Store` на `storeutil.Base`, интерфейс в `internal/dependency/dependency.go`,
wiring в `internal/store/store.go` (обе ветки — пул и tx), моки через mockery.

## Пакет `internal/store/accounting`

Файлы:

```
internal/store/accounting/
  accounting.go     — New(), Store, CreateJournalEntry, ReverseJournalEntry, аккаунты
  ledger.go         — чтение журнала: ListJournalEntries, GetJournalEntry, AccountLedger
  periods.go        — EnsurePeriod, ClosePeriod, ReopenPeriod, guard'ы
  events.go         — outbox: EnqueueEvent, ListPendingEvents, MarkEventProcessed/Failed
  checkpoints.go    — GetCheckpoint / SetCheckpoint
  reports.go        — TrialBalance, ProfitLoss, BalanceSheet (см. 06-reports.md)
  reconcile.go      — сверка ledger ↔ операционные данные (см. 06-reports.md)
```

Конструктор — по образцу `metrics.New` (`internal/store/metrics/metrics.go:21`):

```go
func New(base storeutil.Base, repo dependency.Repository) *Store
```

(саб-сторы получают `storeutil.Base{DB, Now}` и через это работают и на пуле, и внутри `ltx`;
`repo` нужен для кросс-доменных чтений при постинге — заказы, движения, runs).

## Интерфейс `dependency.Accounting`

В `internal/dependency/dependency.go`, в общий блок `type (...)`, рядом с `MaterialStock`:

```go
Accounting interface {
    ContextStore

    // --- план счетов ---
    ListAccounts(ctx context.Context, includeArchived bool) ([]entity.AcctAccount, error)
    CreateAccount(ctx context.Context, in entity.AcctAccountInsert) (int, error)
    UpdateAccountName(ctx context.Context, code, name string) error     // код и section неизменяемы
    SetAccountArchived(ctx context.Context, code string, archived bool) error // is_system → ErrAcctSystemAccount

    // --- журнал ---
    // CreateJournalEntry — ЕДИНСТВЕННАЯ точка записи в журнал (и для автопостинга, и для manual).
    // Валидирует: >=2 строк, Σdebit == Σcredit, amount > 0, счета существуют и не archived,
    // период occurred_at открыт (ErrAcctPeriodClosed), source_key непуст.
    // Идемпотентность: на дубликат (source_type, source_key) возвращает существующий id
    // и alreadyExists=true (через IsErrUniqueViolation, без ошибки — паттерн upsert-ов склада).
    CreateJournalEntry(ctx context.Context, in entity.AcctJournalEntryInsert) (id int, alreadyExists bool, err error)
    // ReverseJournalEntry — сторно: создаёт зеркальную проводку (стороны перевёрнуты) в
    // текущем открытом периоде (occurredAt = сегодня, если период исходной уже закрыт),
    // source_type='reversal', source_key='rev:'+<origID>; проставляет reversed_by у исходной.
    // Повторное сторно той же проводки → ErrAcctAlreadyReversed.
    // Сторно сторно-проводки (source_type='reversal') запрещено → ErrAcctCannotReverseReversal
    // (иначе цепочки rev:rev:... и путаница в отчётах; правится новой прямой проводкой).
    ReverseJournalEntry(ctx context.Context, entryID int, reason, adminUsername string) (int, error)

    ListJournalEntries(ctx context.Context, f entity.AcctEntryFilter) ([]entity.AcctJournalEntry, int, error)
    GetJournalEntry(ctx context.Context, id int) (*entity.AcctJournalEntryFull, error) // entry + lines

    // --- периоды ---
    EnsurePeriodOpen(ctx context.Context, month time.Time) error // lazy-создание строки periода
    ClosePeriod(ctx context.Context, month time.Time, adminUsername string) error
    ReopenPeriod(ctx context.Context, month time.Time, adminUsername string) error
    ListPeriods(ctx context.Context) ([]entity.AcctPeriod, error)

    // --- outbox / чекпоинты (используются продьюсерами и воркером) ---
    // EnqueueEvent маршалит ev.Payload (any → JSON) сам; ошибка маршалинга возвращается
    // (продьюсер в горячей Tx обязан её пробросить). Дубль (event_type, source_key) — no-op
    // (INSERT ... ON DUPLICATE KEY UPDATE id=id).
    EnqueueEvent(ctx context.Context, ev entity.AcctEventInsert) error
    // ListPendingEvents: processed_at IS NULL AND (next_retry_at IS NULL OR next_retry_at <= NOW())
    // ORDER BY id LIMIT :limit
    ListPendingEvents(ctx context.Context, limit int) ([]entity.AcctEvent, error)
    MarkEventProcessed(ctx context.Context, id int64) error
    // MarkEventFailed: attempts++, last_error, next_retry_at = NOW() + retryAfter
    MarkEventFailed(ctx context.Context, id int64, errMsg string, retryAfter time.Duration) error
    // GetCheckpoint: отсутствие строки — НЕ ошибка, вернуть нулевой AcctCheckpoint
    // (воркер трактует как last_id=0 / last_ts=accounting.start_date — первый прогон).
    GetCheckpoint(ctx context.Context, source string) (entity.AcctCheckpoint, error)
    SetCheckpoint(ctx context.Context, source string, lastID sql.NullInt64, lastTS sql.NullTime) error

    // --- отчёты (контракты в 06-reports.md) ---
    GetTrialBalance(ctx context.Context, from, to time.Time) (*entity.AcctTrialBalance, error)
    GetProfitLoss(ctx context.Context, from, to time.Time) (*entity.AcctProfitLoss, error)
    GetBalanceSheet(ctx context.Context, asOf time.Time) (*entity.AcctBalanceSheet, error)
    GetAccountLedger(ctx context.Context, code string, f entity.AcctLedgerFilter) (*entity.AcctAccountLedger, error)
    GetReconciliation(ctx context.Context, from, to time.Time) (*entity.AcctReconciliation, error)

    // --- чтение фактов для воркера (accounting-стор читает чужие таблицы напрямую,
    //     прецедент internal/store/metrics; SQL — в 09-implementation-notes.md §9.2) ---
    GetOrderFactsForPosting(ctx context.Context, orderUUID string) (*entity.AcctOrderFacts, error)
    ListUnpostedMovements(ctx context.Context, afterID int64, startDate time.Time, limit int) ([]entity.AcctMovementFacts, error)
    ListUnpostedReceivedRuns(ctx context.Context, startDate time.Time, limit int) ([]int, error)
    GetRunFactsForPosting(ctx context.Context, runID int) (*entity.AcctRunFacts, error)   // production_run_cost + costed issues
    ListChangedOpexMonths(ctx context.Context, afterTS time.Time) ([]time.Time, error)
    GetOpexMonthFacts(ctx context.Context, month time.Time) ([]entity.AcctOpexCategorySum, error)
}
```

Facts-типы (`entity/accounting.go`): `AcctOrderFacts` (заголовок + items с unit_cost),
`AcctMovementFacts` (движение + имя материала), `AcctRunFacts` (manual-статьи base +
Σ costed issues/returns), `AcctOpexCategorySum` — плоские структуры под SQL из 09;
билдеры `internal/accounting` принимают их на вход.

Ошибки-сентинелы (`internal/store/accounting/accounting.go`):
`ErrAcctUnbalanced`, `ErrAcctPeriodClosed`, `ErrAcctPeriodNotReady`, `ErrAcctUnknownAccount`,
`ErrAcctArchivedAccount`, `ErrAcctSystemAccount`, `ErrAcctAlreadyReversed`,
`ErrAcctCannotReverseReversal` — по образцу `ErrProductionRunHasOpenIssues`
(экспортируемые `errors.New`, apisrv мапит в `codes.FailedPrecondition`/`InvalidArgument`).

## Ключевые реализации

### CreateJournalEntry

```go
func (s *Store) CreateJournalEntry(ctx context.Context, in entity.AcctJournalEntryInsert) (int, bool, error) {
    // 1. валидация формы: len(Lines) >= 2; каждый amount > 0; Σdebit.Equal(Σcredit) — иначе ErrAcctUnbalanced
    // 2. резолв кодов счетов одним SELECT ... WHERE code IN (...) — кэшировать в map[string]int
    //    на процессе НЕ нужно (счетов ~30, запрос копеечный, зато нет инвалидации)
    // 3. EnsurePeriodOpen(firstOfMonth(in.OccurredAt)); если период closed → ErrAcctPeriodClosed
    // 4. INSERT entry; на 1062 (IsErrUniqueViolation) по uniq_acct_entry_source →
    //    SELECT id существующей, return (id, true, nil)
    // 5. bulk INSERT lines — storeutil.BulkInsert (как production_run_cost, productionrun.go:669)
}
```

Все запросы пакета — через `storeutil.QueryNamedOne[T]` / `storeutil.QueryListNamed[T]` /
`storeutil.BulkInsert` (общий стиль репозитория).

Вызывается всегда внутри `Tx` вызывающей стороны; сам метод транзакцию не открывает,
работает на `Base.DB` — стандартная семантика саб-сторов («runs on the caller's connection»).
Двое вызывающих, оба обязаны обернуть: воркер (`repo.Tx { CreateJournalEntry + Mark/Checkpoint }`,
07) и **apisrv-хендлеры ручных проводок** (`s.repo.Tx(func(ctx, rep) { rep.Accounting().
CreateJournalEntry(...) })` — entry+lines это два INSERT'а, без Tx они не атомарны). То же для
`ReverseJournalEntry` (insert сторно + update `reversed_by` исходной).

Важно про идемпотентность + периоды: проверка периода идёт ДО вставки, поэтому автопостинг в
закрытый период упадёт с `ErrAcctPeriodClosed` и останется в outbox/за чекпоинтом → всплывёт в
health и сверке. Это осознанно: закрыли период — не должно быть опоздавших событий (события
приходят с задержкой минуты, закрытие месяца — числа 5-го следующего).

### ClosePeriod

Валидации перед закрытием (всё в одной Tx):
1. `month` — первое число, месяц полностью в прошлом (нельзя закрыть текущий).
2. Нет необработанных `acct_event` с `occurred_at` в месяце.
3. Pull-чекпоинты прошли конец месяца: нет незапощенных `material_stock_movement`
   (`id > checkpoint.last_id AND created_at < начало след. месяца` — count == 0), нет
   received-ранов без проводки, нет opex_line месяца без entry (запросы из `reconcile.go`).
4. Trial balance месяца сбалансирован (Σdebit == Σcredit — инвариант, но проверяем).
Иначе — `ErrAcctPeriodNotReady` с описанием, что именно не готово (в тексте ошибки).

### Правки в `internal/store/store.go`

По образцу `metrics`:
- поле `accounting *accounting.Store` в `MYSQLStore`;
- в `initSubStores(ms)` (`store.go:373`): `ms.accounting = accounting.New(base, ms)` (рядом с
  `ms.metrics = metrics.New(base, ms)`, :386);
- **симметрично в `initSubStoresForTx(txStore, outerTx)`** (`store.go:402`):
  `txStore.accounting = accounting.New(base, txStore)` (рядом с
  `txStore.metrics = metrics.New(base, txStore)`, :415) — иначе `rep.Accounting()`
  внутри `Tx` будет nil — классическая ловушка этого паттерна;
- аксессор `func (ms *MYSQLStore) Accounting() dependency.Accounting { return ms.accounting }`;
- метод `Accounting() Accounting` в интерфейс `Repository` (dependency.go, блок аксессоров).

### Моки

`make generate` (mockery, `.mockery.yaml` с `all: true` на пакет dependency) сам создаст
`internal/dependency/mocks/mock_Accounting.go` и перегенерит `mock_Repository.go`. Проверить,
что `make run` проходит (на version-mismatch mockery — `make run-quick` и отдельно разобраться,
как обычно).

## Тесты (store integration, реальная MySQL — харнесс `internal/store/mysql_test.go`)

`internal/store/accounting_core_integration_test.go`:
- сбалансированная проводка создаётся, несбалансированная → `ErrAcctUnbalanced`;
- дубль `(source_type, source_key)` → тот же id, `alreadyExists=true`, строки не задвоены;
- постинг в закрытый период → `ErrAcctPeriodClosed`; reopen → постится;
- сторно: зеркальные строки, `reversed_by` проставлен, второе сторно → `ErrAcctAlreadyReversed`;
- archived/unknown счёт → ошибки; `SetAccountArchived` на `is_system` → `ErrAcctSystemAccount`;
- ClosePeriod с pending event → `ErrAcctPeriodNotReady`;
- trial balance после N случайных сбалансированных проводок: Σdebit == Σcredit, счета сходятся.

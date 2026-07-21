# Часть 7: воркер `acctposting`, конфиг, wiring в app

## Пакет `internal/acctposting`

Строго по канону воркеров репозитория (`ordercleanup` — эталон структуры; `opexmaterialize` —
эталон «materialize»-семантики):

```
internal/acctposting/
  acctposting.go   — Config, DefaultConfig, Worker, New, Start, Stop, Name, LastSuccess
  worker.go        — цикл: ticker, runOnce, consecutiveFailures→backoff, saferun.Recover,
                     tickTimeout, tracker.MarkSuccess/MarkError
  outbox.go        — обработка acct_event (order_paid / order_refund)
  pull.go          — сканы movements / production receives / opex
```

```go
type Config struct {
    Enabled        bool          `mapstructure:"enabled"`
    WorkerInterval time.Duration `mapstructure:"worker_interval"`   // дефолт 1m
    BatchSize      int           `mapstructure:"batch_size"`        // дефолт 200
    StartDate      string        `mapstructure:"start_date"`        // 'YYYY-MM-DD' cutover; обязателен при Enabled
    SettledWaitMax time.Duration `mapstructure:"settled_wait_max"`  // дефолт 48h — алерт-порог зависших order_paid
}

func New(c *Config, repo dependency.Repository) *Worker
```

`Name() = "acctposting"`, регистрируется в health-registry (`/statusz`) как остальные.

### Правило локов: читать на пуле, писать в Tx (ВАЖНО)

`repo.Tx` работает на **SERIALIZABLE** (`internal/store/db.go:156`). В InnoDB под SERIALIZABLE
обычные SELECT'ы берут shared next-key локи: скан `material_stock_movement WHERE id > :last_id`
внутри Tx повесил бы gap-лок на хвост таблицы и **блокировал бы приход/выдачу материала** на
время тика. Поэтому паттерн каждой единицы работы строго такой:

1. **Чтение фактов — на пуле, вне Tx** (`GetOrderFactsForPosting`, `ListUnpostedMovements`,
   `GetRunFactsForPosting`, `GetOpexMonthFacts` — все работают на `Base.DB` корневого стора).
2. **Построение проводки — чистый билдер** (`internal/accounting`), без БД.
3. **Запись — короткая `repo.Tx`**: только `CreateJournalEntry` + `MarkEventProcessed` /
   `SetCheckpoint`. Никаких чтений исходных таблиц внутри.

Это безопасно, потому что источники append-only/иммутабельны для прочитанного среза (движения
не редактируются, заказ после confirmed меняет только статусные поля), а идемпотентность по
`(source_type, source_key)` гасит любой повтор при гонке «прочитал → упал → перечитал».

### runOnce — порядок фаз (каждая изолирована, ошибка фазы не роняет следующие)

1. **Outbox**: `ListPendingEvents(batch)` (пул); каждое событие обрабатывается по правилу
   локов выше — факты на пуле, запись короткой Tx:
   - `order_paid`: `GetOrderFactsForPosting` (пул) → готов? → `Build*`
     (`internal/accounting`) → `repo.Tx { rep.Accounting().CreateJournalEntry +
     MarkEventProcessed }`.
     Не готов (Stripe-заказ, settled ещё NULL) → `MarkEventFailed("settled pending", 5*time.Minute)`
     — это ожидание, не сбой: не учитывать в consecutive-failures воркера; событие старше
     `SettledWaitMax` → health-warning.
   - `order_refund`: payload → `BuildOrderRefundEntry` → создать → processed.
   - Ошибка постинга (бага) → `MarkEventFailed(err, retryAfter)`, где
     `retryAfter = min(1m × 2^attempts, 6h)`; выборка pending идёт по
     `processed_at IS NULL AND (next_retry_at IS NULL OR next_retry_at <= NOW()) ORDER BY id`
     — backoff хранится в строке (`next_retry_at`), не вычисляется в SQL.
2. **Movements**: `GetCheckpoint("material_movement")` (нет строки → last_id=0, скан от
   `start_date`) → батч движений `> last_id` **читается на пуле** → для каждого: costed →
   entry (M1–M8, clamp occurred_at — см. 03); uncosted-денежное → пропуск. Затем одна
   короткая Tx: вставка всех entries батча + `SetCheckpoint` (атомарность «запостили и
   сдвинулись», исходные таблицы внутри Tx не читаются). Идемпотентность по source_key
   позволяет безопасный повтор при падении между батчами.
3. **Production receives**: скан received-без-проводки → по одному в Tx (`BuildProductionReceiveEntry`
   читает `production_run_cost` + costed-issues руна тем же запросом, что валютация WIP).
4. **OPEX**: чекпоинт `opex_line` по `updated_at` → изменённые месяцы → repost открытых
   месяцев (reverse+create), закрытые — warning.
5. `tracker.MarkSuccess()` если фазы 1–4 прошли без ошибок (пропуски/ожидания — не ошибки).

Инкрементальность: один тик — ограниченные батчи; хвост дожуют следующие тики (интервал 1m).
Конкуренции нет — воркер единственный (одна реплика приложения; если появится
горизонтальное масштабирование — блокировка через `GET_LOCK`, но это не сегодняшняя проблема).

### Health-алерты (в `buildDashboardAlerts`, `internal/store/metrics/dashboard.go`)

По образцу `low_material_stock` / `stale_open_production_run` добавить коды:
- `acct_posting_lag` — есть события/движения старше N часов без проводки (N из
  alert_setting, дефолт 24h);
- `acct_manual_entry_required` — заказы, помеченные «требует ручной проводки»;
- `acct_reconciliation_drift` — сверочная дельта revenue/fees выше порога.
Данные — из готовых запросов `reconcile.go`. (`AlertSettings` при желании расширить полем
`acct_posting_lag_hours` — по образцу `production_run_stale_days`.)

## Конфиг (`config/cfg.go`)

1. Поле: `Accounting acctposting.Config \`mapstructure:"accounting"\``.
2. `bindEnvVars()`:
   ```go
   viper.BindEnv("accounting.enabled",          "ACCOUNTING_ENABLED")
   viper.BindEnv("accounting.worker_interval",  "ACCOUNTING_WORKER_INTERVAL")
   viper.BindEnv("accounting.batch_size",       "ACCOUNTING_BATCH_SIZE")
   viper.BindEnv("accounting.start_date",       "ACCOUNTING_START_DATE")
   viper.BindEnv("accounting.settled_wait_max", "ACCOUNTING_SETTLED_WAIT_MAX")
   ```
3. `Config.Validate()`: при `Enabled` — `StartDate` парсится и не в будущем.
4. `.do/app.yaml` (prod) и `.do/app-beta.yaml` (beta, gitignored): `ACCOUNTING_ENABLED`
   (сначала true только на бете), `ACCOUNTING_START_DATE`.

## Wiring в `app/app.go`

- Поле `ap *acctposting.Worker` в `App`.
- `Start`: создать/запустить **после** `store.New` и после `cache.SetDefaultCurrency(...)`
  (постинг зависит от base currency так же, как `opexmaterialize` — вставить рядом с ним),
  gated `cfg.Accounting.Enabled` (паттерн `fxsync`/`ga4sync`):
  ```go
  if a.cfg.Accounting.Enabled {
      a.ap = acctposting.New(&a.cfg.Accounting, a.db)
      if err := a.ap.Start(ctx); err != nil { ... }
  }
  ```
- Health: в `buildHealthRegistry` (`app/app.go:564`) добавить `if a.ap != nil { addWorker(a.ap) }`
  — воркеры регистрируются там, nil-guarded, а не через self-register.
- `Stop`: добавить `a.ap` в цепочку остановки воркеров **до** `db.Close()` (nil-guarded, рядом
  с `om`/`fxw`).

Продьюсеры outbox (`internal/store/order`) от флага не зависят: события копятся всегда с момента
деплоя миграций — включение воркера позже просто дожуёт очередь. `StartDate` отсекает pull-скан
движений до cutover.

## Тесты

- `internal/accounting/*_test.go` — юнит: каждый билдер (S1/S2/M*/P1/O1) на фикстурах:
  баланс Dr=Cr, счета, суммы, caveat-условия, копеечные пропорции (свойство: при любых
  vat/ship/fee долях entry сбалансирован — table-driven + пара рандомизированных кейсов).
- `internal/store/acctposting_integration_test.go` — интеграция: фикстурный заказ → событие →
  обработка → проверка entry; движение склада → M1; run receive → P1; opex → O1 и repost;
  повторный прогон runOnce — нет дублей (идемпотентность); событие с settled=NULL остаётся
  pending и постится после дозаписи settled.

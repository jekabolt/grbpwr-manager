# Часть 9: справочник имплементатора — проверенные факты, сниппеты, FAQ

Этот файл экономит агенту-имплементатору поиск по кодовой базе. Всё ниже **проверено против
кода** на момент написания плана (июль 2026). Если строка/сигнатура не совпала — код уехал,
доверять коду, а план поправить. Перед стартом обязательно прочитать `CLAUDE.md` (он же
`AGENTS.md`) в корне репо — правила миграций, «два EUR-показателя», «warehouse vs COGS»
напрямую ограничивают этот модуль.

## 9.1 Карта проверенных фактов (grep-верифицировано)

| Что | Где именно |
|---|---|
| `OrderPaymentDone` | `internal/store/order/payment.go:476`, `func (s *Store) OrderPaymentDone(ctx, orderUUID string, p *entity.Payment) (bool, error)`; тело — `s.txFunc(ctx, func(ctx, rep dependency.Repository) error {...})`; точка вставки события — после `wasUpdated = true` |
| `RefundOrder` | `internal/store/order/lifecycle.go:235` (НЕ refund.go); в scope: `order`, `refundedAmount`, `refundedByItem map[int]int64`; вставка — после `updateOrderStatusAndAccumulateRefundedAmount` |
| `refundAmountFromItems` | `internal/store/order/refund.go:18` |
| Конструктор order-стора | `internal/store/order/store.go:39` `New(base storeutil.Base, txFunc TxFunc, repFunc RepFunc)` |
| Конструктор metrics-стора (образец для accounting) | `internal/store/metrics/metrics.go:21` `New(base storeutil.Base, repo dependency.Repository)` |
| Wiring саб-сторов | `internal/store/store.go:386` (`ms.metrics = metrics.New(base, ms)`) и tx-ветка `:415` (`txStore.metrics = metrics.New(base, txStore)`) |
| Методы оплаты | `internal/entity/payment.go:71-74`: `CARD "card"`, `CARD_TEST "card-test"`, `BANK_INVOICE "bank-invoice"`, `CASH "cash"`; `ValidPaymentMethodNames` :78 |
| Имя метода по id | `cache.GetPaymentMethodById(id)` (`internal/cache/cache.go:462`) |
| Базовая валюта | `cache.GetBaseCurrency()` (`cache.go:604`), ставится в `app.Start` через `SetDefaultCurrency` |
| Поля заказа | `entity.Order`: `TotalPrice`, `Currency`, `TotalSettledBase decimal.NullDecimal (db:"total_settled_base")`, `PaymentFee NullDecimal`, `VatRatePct/VatAmount NullDecimal` (`internal/entity/order.go:69-107`) |
| `cost_price_at_sale` | В entity **отсутствует**! Пишется raw-мапой в `internal/store/order/insert.go:167,173`, читается только прямым SQL (`internal/store/metrics/margin.go:77` — образец COALESCE-fallback) |
| `OrderFull` | `internal/entity/order.go:49` (Order, OrderItems, Payment, Shipment, Buyer, ...) — для UI-нужд; для постинга НЕ использовать (нет cost-полей) |
| Стоимость доставки | `entity.Shipment.CostDecimal(currency)` (`internal/entity/shipment.go:228`) |
| Костинг-курсы | `dependency.TechCards.GetCostingFxRatesToBase(ctx) (map[string]decimal.Decimal, error)` (`dependency.go:606`) — эффективные as-of сегодня |
| Admin username | `authsrv.GetAdminUsername(ctx)` (`internal/apisrv/auth/auth.go:470`), импорт `authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"` — образец использования `internal/apisrv/admin/inventory.go:33` |
| Воркер-эталон | `internal/ordercleanup/` (Config/Worker/New/Start/Stop + `worker.go` с backoff); паника — `defer saferun.Recover(ctx, "name")` (`internal/ordercleanup/worker.go:80`) |
| Health | `internal/health`: `Reporter` (Name/LastSuccess), `Tracker.MarkSuccess/MarkError`; регистрация — `app/app.go:564 buildHealthRegistry`, `addWorker(a.X)` nil-guarded |
| Алерты дашборда | `internal/store/metrics/dashboard.go`: `buildDashboardAlerts` :314, образцы кодов :376/:390; пороги `entity.AlertThresholds` (`entity/metrics.go:831`) + `Metrics().GetAlertThresholds` |
| OPEX-категории | `entity.ValidOpexCategories` (`entity/metrics.go:1323`) — 10 штук |
| OPEX-стор | `internal/store/metrics/opex_v2.go`: `UpsertOpexLines:103`, `DeleteOpexLine:136`, `ListOpexLines:155`, `MaterializeOpexRecurring:247` |
| Производств. затраты | `internal/store/productionrun/productionrun.go`: `loadRunCosts:615` (private — accounting читает `production_run_cost` своим SQL), `GetProductionRun:454`, `ReceiveProductionRun:216`; `entity.ProductionRun.ActualUnitCostBase()` (`entity/productionrun.go:264`) |
| SQL-хелперы | `storeutil.QueryNamedOne[T]`, `storeutil.QueryListNamed[T]`, `storeutil.BulkInsert` (используются повсеместно, см. productionrun.go) — писать запросы ими |
| HTTP-биндинги proto | префикс `/api/admin/...` (примеры `proto/admin/admin/admin.proto:38+`) |
| RBAC completeness | `internal/rbac/rbac_test.go` — упадёт на незамапленном RPC |
| Статусы для revenue-запросов | `cache.OrderStatusIDsForNetRevenue()` / `OrderStatusIDsForRefund()` (`cache.go:303,316`) — использовать в сверке |
| Идемпотентный дуп-чек | `Repository.IsErrUniqueViolation(err)` |
| `storeutil.Base` | `internal/store/storeutil/base.go:13`: `{ DB dependency.DB; Now func() time.Time }` |
| `dependency.ContextStore` | `dependency.go:23`: единственный метод `Tx(ctx, fn)` — Accounting его встраивает, как все домены |
| Образец структуры стора | `metrics.Store` (`internal/store/metrics/metrics.go:15`): `{ storeutil.Base; repo dependency.Repository }` — accounting.Store копирует один в один |
| `CreateCustomOrder` | `internal/store/order/create.go:127`; тело — `s.txFunc(ctx, func(ctx, rep) {...})`; заказ рождается Confirmed, `OrderPaymentDone` НЕ вызывается → третий продьюсер события (03) |
| `health.Registry` | `internal/health`: `{ Workers []Reporter; DB; Breakers }`; воркер добавляется в `app/app.go:564 buildHealthRegistry` через `addWorker(a.ap)` |
| `app.App` поля воркеров | `app/app.go:55` — `oc, dsw, sc, tm, om, sr, fxw, ga4w`; поле `ap *acctposting.Worker` добавить рядом с `om` |
| Swagger-генерация | Makefile `internal/statics` (:54): собирает `proto/swagger/*.json` → `internal/api/http/static/swagger/api.swagger.json`; входит в `make build` |
| `google.type.Decimal` | доступен через buf-зависимость `buf.build/googleapis/googleapis` (`proto/admin/buf.yaml:4`) — ничего вендорить не нужно |
| RBAC-регистрация | `internal/rbac/rbac.go`: секции-каталог `var catalog` :79 (добавить строку SectionAccounting), `methodRequirements` :137, хелперы `rd(section)` :130 / `wr(section)` :131 |
| Коллизии имён RPC | `TrialBalance|JournalEntry|BalanceSheet|AcctPeriod|ProfitLoss` в admin.proto — 0 совпадений, имена свободны |
| `config.Validate` | `config/cfg.go:207` — сюда проверку `accounting.start_date` |
| Изоляция `Tx` | SERIALIZABLE (`internal/store/db.go:156`) → воркер обязан читать факты на пуле, писать короткой Tx (07, «правило локов») |
| Отмена оплаченного заказа | НЕВОЗМОЖНА без рефанда: `cancelOrder` (`internal/store/order/refund.go:60`) явно отвергает Confirmed через `validateOrderStatusNot(..., entity.Confirmed)` — «sale posted, но денег не вернули» не бывает, рефанд ловится своим событием |
| Fee-модель методов оплаты | `entity.PaymentMethod.FeePct/FeeFixed` (`entity/payment.go:92-93`); формула оценки — `base × fee_pct/100 + fee_fixed, GREATEST(...,0)` (образец: `metrics/shipping.go:66`); у cash/bank-invoice по умолчанию 0 |
| Имена init-функций | `initSubStores(ms)` `store.go:373`, `initSubStoresForTx(txStore, outerTx)` `store.go:402` |
| Префикс `acct_` | в `internal/store/sql/` не занят (0 совпадений) |
| migrationlint внутренности | `grandfatheredMigrationMax = 92`; idempotency-линт проверяет ТОЛЬКО `CREATE TABLE IF NOT EXISTS` и запрет `DROP CHECK *_chk_<n>`; идемпотентность INSERT'ов не линтуется (ревью + двойной прогон); `enum_drift_test` — ручные пары test-функций (см. 01) |
| SQL миграций из 01 | прогнан лексическим чекером: 34 seed-строки арности 5, скобки сбалансированы, DROP-порядок уважает FK, CHECK'и именованы; шаблон `INSERT ... SELECT ... WHERE NOT EXISTS` — точный аналог применённого 0112 |
| adjustment/writeoff несут стоимость | `adjustInTx` замораживает `UnitCostBase: before.Avg` (`internal/store/inventory/inventory.go:625`) для обоих типов; quantity в движении = abs(delta), знак — из on_hand_after−on_hand_before. M7/M8 постятся у costed-материалов |
| Даты в proto | оба прецедента в admin.proto: `Timestamp from/to` (:2136) и строки `YYYY-MM-DD` (:4613, OPEX) — accounting использует строковый (решение в 05) |
| `wg.Go` в воркерах | реально используется (`ordercleanup.go:81`, Go 1.26) — копировать каркас смело |
| `order.UUID` в CreateCustomOrder | заполнен к точке вставки события — существующий код уже использует `order.UUID` в том же замыкании (`StockHistoryParams{OrderUUID: order.UUID}`) |
| Пакет `internal/accounting` | не существует — имя свободно. Одноимённость с `internal/store/accounting` не проблема: ни один файл не импортирует оба (воркер ходит в store через `dependency.Repository`, root-store не импортирует билдеры) |

## 9.2 Готовый SQL: order facts для постинга S1/S2

В `internal/store/accounting` (accounting-стор читает чужие таблицы напрямую — прецедент:
весь `internal/store/metrics`). Один заказ:

```sql
-- заголовок
SELECT co.id, co.uuid, co.placed, co.total_price, co.currency,
       co.total_settled_base, co.payment_fee, co.vat_amount, co.vat_rate_pct,
       p.payment_method_id,
       s.cost AS shipment_cost, s.free_shipping
FROM customer_order co
JOIN payment  p ON p.order_id = co.id
LEFT JOIN shipment s ON s.order_id = co.id
WHERE co.uuid = :uuid;

-- строки: COGS-факты (fallback идентичен metrics/margin.go)
SELECT oi.id, oi.product_id, oi.quantity,
       COALESCE(oi.cost_price_at_sale, pr.cost_price) AS unit_cost
FROM order_item oi
JOIN product pr ON pr.id = oi.product_id
WHERE oi.order_id = :order_id;
```

Go-структуры фактов (`internal/store/accounting/orderfacts.go`) с `db:`-тегами под эти
запросы; из них собирается вход для билдеров `internal/accounting`.

Дата проводки S1: не `DATE(co.placed)` — момент признания = подтверждение оплаты; брать
`occurred_at` события outbox (`acct_event.occurred_at`, оно = `s.Now()` в момент
`OrderPaymentDone`). Для S2 — аналогично из события.

## 9.3 Конвенции (обязательны, из CLAUDE.md + фактического кода)

- Ошибки: `fmt.Errorf("...: %w", err)`; логи `slog` с `ctx` и `slog.String("err", err.Error())`.
- Деньги: `shopspring/decimal`; **никаких float**. Округление `.Round(2)` только финальных
  строк проводки; балансирующая строка = разность (см. 04).
- Даты: `occurred_at` — `DATE` в таймзоне UTC; в воркере всегда `repo.Now().UTC()`
  (в Tx `Now()` заморожен — использовать его, не `time.Now()`).
- Новые SQL-запросы — через `storeutil.QueryNamedOne/QueryListNamed/BulkInsert`.
- Интерфейс + мок: метод сначала в `internal/dependency/dependency.go`, потом `make generate`.
- Не редактировать `*.pb.go`/`*.gw.go` руками; после правки proto — `make proto` (или
  `make build`, он включает всё + swagger).
- Миграции: следующий свободный номер проверить непосредственно перед PR
  (`ls internal/store/sql | sort | tail`); файл не переименовывать после мерджа.
- В горячих путях (`OrderPaymentDone`, `RefundOrder`) — никакой сети/тяжёлой работы, только
  INSERT в outbox; ошибку вставки возвращать (Tx откатится — это осознанно, см. 03).
- `internal/accounting` (билдеры) — без импорта store/sqlx: чистые функции на entity-типах.

## 9.4 FAQ имплементатора (решения приняты, не переспрашивать)

1. **Почему не постим прямо из Tx заказа, без outbox?** Settled base приходит асинхронно
   (`topUpSettledBase`), и постинг не должен уметь ронять оплату. Outbox = буфер + ретраи +
   replay. Решение финальное.
2. **Что если у заказа нет `vat_amount` (NULL)?** VAT-строка опускается, вся сумма в выручку,
   `has_caveat=true`. Не пытаться пересчитать VAT самим — фаза 2.
3. **Заказ оплачен до `ACCOUNTING_START_DATE`, а рефанд после?** Рефанд-событие постится
   (сумма точная), sale-проводки нет — сверка покажет асимметрию; это принятая цена решения
   «старт с нуля». В отчёте сверки такие рефанды помечаются `pre-cutover order`.
4. **`total_settled_base` есть, но валюта заказа EUR и суммы совпадают?** Никакой спец-логики:
   всегда settled, если есть; `k = G/total_price` тогда ≈ 1.
5. **Изменили `product.cost_price` задним числом — пересчитывать проводки?** Нет. Леджер
   append-only, COGS зафиксирован снапшотом на момент продажи (правило CLAUDE.md). Дельта
   видна в сверке FG.
6. **Движение склада отредактировали/удалили?** Движения append-only, удалений нет
   (`adjustment` — корректирующее движение, придёт своим чередом). Ничего делать не надо.
7. **Один `acct_event` обрабатывается, воркер упал между entry и MarkEventProcessed?**
   Обработка события идёт в одной `repo.Tx` (entry + mark вместе) — состояние «entry есть,
   event pending» невозможно. Если Tx всё же повторится — `alreadyExists=true` спасает.
8. **Куда писать «инбокс неразобранного» (лист Unsorted)?** Фаза 1: это сверочный отчёт
   (блок Pending/Unposted в `GetAcctReconciliation`) + ручные проводки. Отдельной таблицы
   инбокса нет.
9. **Нужен ли отдельный счёт под каждую валюту кошелька?** Нет (фаза 1 — всё EUR). USDT/крипта
   — фаза 2, счёт 1050.
10. **`acct_event.payload` — JSON или колонки?** JSON (`json.RawMessage`), типизированные
    структуры `AcctOrderPaidPayload`/`AcctOrderRefundPayload` в entity. Прецедент JSON-колонки —
    `product.cost_breakdown` (0100).
11. **Sequence для refund source_key** — `SELECT COUNT(*) FROM acct_event WHERE event_type='order_refund'
    AND source_key LIKE CONCAT(:uuid, ':%')` + 1, в той же Tx. Гонок нет (рефанд — админ-операция
    под `FOR UPDATE` заказа).
12. **Что делает воркер, если период проводки закрыт (позднее событие)?** `CreateJournalEntry`
    вернёт `ErrAcctPeriodClosed`; событие остаётся failed с этой ошибкой → алерт. Разрешение:
    reopen периода админом → воркер допостит → close заново. Автопереноса в текущий период нет
    (осознанно: молчаливый перенос искажает месяц).
13. **`amount_src`/`currency_src` для авто-проводок заполнять?** Для M1 (receipt) можно
    (`unit_cost`+`currency` движения) — полезная трассировка; для остальных NULL.
14. **Гранулярность проводок движений: батчевать в одну entry на тик?** Нет — одна entry на
    движение (source_key = movement id): идемпотентность проще, drill-down читабельнее.
    Объёмы малы.
15. **Кастомные заказы (cash/bank-invoice)** не проходят через `OrderPaymentDone` — событие
    шлёт `CreateCustomOrder` (третий продьюсер, см. 03). Не пытаться «дожать» их через
    `OrderPaymentDone`.
16. **Ручная проводка в USDT** требует строки `costing_fx_rate` для USDT (ECB её НЕ даёт —
    fxsync тянет только ECB reference rates; курс заводится админом через существующий RPC
    `UpsertCostingFxRates`). Нет курса → `InvalidArgument` с текстом «add USDT costing fx rate
    first».
17. **Конвертация ручной проводки задним числом** — по текущему эффективному курсу
    (`GetCostingFxRatesToBase` as-of сегодня), не по историческому на occurred_at. Осознанное
    упрощение фазы 1 (историческую as-of выборку добавит фаза 2 вместе с GBP).
18. **Retention `acct_event`** — не чистим: объём копеечный (события только по заказам),
    это аудит-след. Никаких cleanup-воркеров.
19. **Годовое закрытие / Retained Earnings**: в фазе 1 нет процедуры year-end close. Строка
    «Current Period Net Profit» в BS покрывает весь P&L с cutover — баланс сходится всегда;
    счёт 3020 остаётся нулевым до фичи закрытия года (фаза 2). Это не баг.
20. **OPEX-месяц опустел** (все строки удалены): воркер сторнирует существующую entry и НЕ
    создаёт новую. Идемпотентность: сторно уже есть → no-op.
21. **Fee для cash/bank-invoice**: по модели `payment_method.fee_pct/fee_fixed` (обычно 0 —
    строки fee просто нет). Не хардкодить ноль — читать модель, как metrics.
22. **Admin отменил оплаченный заказ → дырка "выручка без денег"?** Невозможно:
    `cancelOrder` отвергает Confirmed (см. таблицу фактов). Единственный путь размотки
    оплаченного заказа — RefundOrder → событие S2.
23. **Почему воркер не читает источники внутри Tx** — SERIALIZABLE next-key локи заблокировали
    бы горячие вставки склада; см. «правило локов» в 07. Нарушение этого правила — блокер
    ревью PR5.
24. **Рефанд раньше продажи** (рефанд из Confirmed, пока sale-событие ждёт settled): S2
    обрабатывается только при существующей S1-проводке заказа, иначе defer
    `awaiting sale posting` (03/04). Рефанды pre-cutover заказов остаются висеть с этой
    меткой — разносятся вручную вместе с продажей.
25. **Движение задним числом**: `entry.occurred_at = max(movement.occurred_at, start_date)`;
    попадание в закрытый период → clamp к текущему открытому + caveat `backdated movement`
    (правило только для pull-источников — outbox-события ждут reopen, FAQ 12).
26. **Ручные проводки через API — только в `s.repo.Tx`** (пример-обёртка в 05): entry+lines —
    два INSERT'а; без Tx возможна полупроводка. Store намеренно не открывает Tx сам —
    композируемость с воркером.
27. **OPEX-месяцы до cutover** игнорируются pull-сканом (`month >= месяц start_date`) — даже
    если их редактируют после включения бухгалтерии.
28. **bank-invoice ≠ деньги**: кастомный заказ с bank-invoice признаётся против
    1040 Accounts Receivable, НЕ против 1010 (инвойс ещё не оплачен). Поступление оплаты —
    ручная MN Dr 1010 / Cr 1040. Рефанд кредитует тот же счёт, что дебетовала S1 (mAcc).
29. **Как воркер находит текущую версию OPEX-проводки месяца**: entries
    `source_type='opex_month' AND source_key LIKE 'YYYY-MM%' AND reversed_by IS NULL` —
    ровно одна (или ноль); новая версия = `'YYYY-MM:v' + (count всех версий месяца + 1)`.
30. **Pre-cutover WIP в P1**: LEDGER_WIP считает только movements с
    `created_at >= start_date` (зеркало запощенного) — иначе Cr 1120 больше, чем туда
    дебетовалось, и WIP уходит в минус. Ран, открытый до cutover'а, получает caveat.
31. **Outbox-события до start_date** — skip с меткой `pre-start event` (03): на проде
    продьюсеры копят события до выбора cutover-даты, эти заказы в леджер не входят.
32. **Момент признания — confirmed, не delivered** — явная учётная политика (04); смена на
    delivered-based — фаза 2 сменой продьюсера, правила не трогаются.
33. **Сверка 1030/1010 с внешним миром** — руками: Stripe Dashboard и банковская выписка,
    ежемесячная рутина в 08. Внутри системы у этих счетов нет независимого источника.
34. **Chargeback/dispute** — события нет (RefundOrder не вызывается): ручная MN-проводка
    (шаблон в 04), всплывает при ручной сверке 1030. Webhook — фаза 2.
35. **Системный баг правил постинга** (месяц неверных проводок): массовое сторно скриптом по
    `source_type` + диапазону дат, фикс билдера, повторный прогон с source_key-суффиксом
    новой версии. Инструмент заранее не строим — append-only модель делает это возможным
    в любой момент.

## 9.5 Acceptance criteria по PR (дополнение к 08)

- **PR1 (schema)**: `make build` зелёный; оба файла миграций применяются на чистой БД и
  повторно (идемпотентность — прогнать дважды локально); `migrationlint` зелёный; seed вернул
  34 счёта (`SELECT COUNT(*) FROM acct_account` == 34: 8 asset (вкл. 1040) + 3 liability +
  3 equity + 4 revenue + 5 cogs (вкл. 6210) + 11 opex).
- **PR2 (store)**: интеграционные тесты из 02 зелёные; `rep.Accounting()` доступен внутри
  `Tx` (тест: `Tx(func(ctx, rep) { rep.Accounting().ListAccounts(...) })`).
- **PR3 (rules)**: юнит-тесты всех билдеров; property-тест «любые пропорции → Dr==Cr».
- **PR4 (producers)**: интеграционный тест: `OrderPaymentDone` → строка в `acct_event`;
  повторный вызов → одна строка; `RefundOrder` → событие с точной суммой;
  `CreateCustomOrder` (cash) → событие `order_paid`; событие в той же Tx (роллбек Tx → нет
  события).
- **PR5 (worker)**: тест-сценарии из 07; `/statusz` показывает `acctposting`; при
  `ACCOUNTING_ENABLED=false` воркер не стартует, приложение живёт как раньше.
- **PR6 (api)**: `rbac_test` зелёный; swagger сгенерирован; ручная проводка через REST
  работает end-to-end на локальной БД; несбалансированная → 400 с внятным сообщением.
- **PR7 (reports)**: сквозной пример из 04 воспроизводится тестом (числа в 06); P&L+BS+TB
  консистентны между собой (NP в BS == OperatingProfit P&L за период).
- **PR8 (alerts)**: при искусственно зависшем событии дашборд показывает `acct_posting_lag`.

## 9.6 Известные ловушки

- Tx-ветка wiring: забыть `txStore.accounting = accounting.New(base, txStore)` — паника
  nil-указателя только на путях внутри Tx (продьюсеры!). Тест из PR2 ловит.
- gRPC-gateway порядок роутов: литеральные пути после `/{id}` (правило из репо, см. 05).
- `decimal.NullDecimal.Decimal` без проверки `.Valid` — тихий ноль. Всегда проверять Valid.
- Payload маршалит `EnqueueEvent` внутри себя (продьюсер передаёт структуру) — но ошибку
  `EnqueueEvent` в продьюсере обязательно пробрасывать (`if err != nil { return err }`):
  мы в платёжной Tx, тихо глотать нельзя.
- В `runOnce` фазы изолировать: ошибка OPEX-фазы не должна блокировать outbox-фазу (см. 07).
- MySQL `DATE` сравнения: `occurred_at < :to` с to = первое число следующего месяца
  (полуинтервал), не `<= last day` — как в metrics-запросах.

# Часть 3: захват событий — push-outbox для заказов, pull для остального

Гибрид. Критерий выбора: **push**, если точную сумму события нельзя восстановить из таблиц
постфактум; **pull**, если источник уже append-only/идемпотентно сканируем.

| Источник | Способ | Почему |
|---|---|---|
| Оплата заказа (Stripe) | push `order_paid` | момент признания выручки = переход confirmed; сканировать `order_status_history` можно, но push дешевле и однозначнее |
| Кастомный заказ (cash/bank-invoice) | push `order_paid` | рождается сразу Confirmed в `CreateCustomOrder` и **не проходит** через `OrderPaymentDone` — нужна своя точка |
| Рефанд | push `order_refund` | сумма ЭТОГО рефанда нигде не хранится — `refunded_amount` копится агрегатом; восстановить разбивку по событиям нельзя |
| Движения материалов | pull | `material_stock_movement` — append-only с монотонным id; чекпоинт по id; ноль правок в коде склада |
| Receive производственной партии | pull | `production_run.received_at` + идемпотентность по `source_key=run_id`; скан «received без проводки» |
| OPEX | pull | `opex_line` мутируем (upsert), нужен repost месяца по `updated_at` |
| Ручные проводки | прямой вызов store из apisrv | не событие |

## 3.1 Push: продьюсеры outbox в `internal/store/order`

Единственные правки горячего кода во всей фиче — **три точки**, каждая по одной вставке в уже
открытую транзакцию.

### `order_paid`

Точка: **`OrderPaymentDone`** (`internal/store/order/payment.go:476`,
`func (s *Store) OrderPaymentDone(ctx, orderUUID string, p *entity.Payment) (bool, error)`) —
это единый sink всех путей подтверждения оплаты (webhook / expiry-monitor / lazy-check сходятся
в `Processor.updateOrderAsPaid` → `rep.Order().OrderPaymentDone`, см.
`internal/payment/stripe/payment.go:249`). Внутри той же Tx, где заказ переводится в
`confirmed`, после успешного апдейта статуса:

```go
// внутри tx-замыкания, сразу после wasUpdated = true (реальный переход, не повтор):
if err := rep.Accounting().EnqueueEvent(ctx, entity.AcctEventInsert{
    EventType:  entity.AcctEventOrderPaid,
    SourceKey:  orderUUID,
    Payload:    entity.AcctOrderPaidPayload{OrderUUID: orderUUID},
    OccurredAt: s.Now(),
}); err != nil {
    return fmt.Errorf("enqueue acct order_paid event: %w", err)
}
```

Механика вызова — **проверено по коду**: `OrderPaymentDone` уже выполняется внутри
`s.txFunc(ctx, func(ctx, rep dependency.Repository) error { ... })` (order-стор создаётся как
`order.New(base, txFunc, repFunc)`, `internal/store/order/store.go:39`). Значит внутри замыкания
доступен tx-scoped `rep` → вставка это просто `rep.Accounting().EnqueueEvent(ctx, ...)` —
та же транзакция, тот же паттерн, что `rep.Products().RestoreStockForProductSizes` в рефанде.
Точное место: сразу после `wasUpdated = true` (перед `return nil` замыкания). Никаких хелперов
в пакете order не нужно. `EnqueueEvent` использует `INSERT ... ON DUPLICATE KEY UPDATE id=id`
(no-op) — повторный вызов безопасен.

### `order_paid` для кастомных заказов (cash / bank-invoice)

**Проверено по коду:** кастомный заказ рождается сразу Confirmed —
`CreateCustomOrder` (`internal/store/order/create.go:127`) сам ставит
`cache.OrderStatusConfirmed` и **не вызывает** `OrderPaymentDone`. Без отдельной точки события
для cash/bank-invoice заказов не будет. Метод тоже работает внутри
`s.txFunc(ctx, func(ctx, rep) {...})` — вставка в конце замыкания, обернув текущий финальный
`return insertOrderStatusHistoryEntry(...)`:

```go
if err := insertOrderStatusHistoryEntry(ctx, txDB, order.Id,
    cache.OrderStatusConfirmed.Status.Id, "admin", "custom order"); err != nil {
    return err
}
return rep.Accounting().EnqueueEvent(ctx, entity.AcctEventInsert{
    EventType:  entity.AcctEventOrderPaid,
    SourceKey:  order.UUID,
    Payload:    entity.AcctOrderPaidPayload{OrderUUID: order.UUID},
    OccurredAt: s.Now(),
})
```

Воркер обрабатывает такие события веткой «не-Stripe» правил готовности (ниже): EUR — постим по
`total_price`, не-EUR — skip с пометкой. Тип события один и тот же — источнику всё равно,
каким путём заказ стал Confirmed.

Обратите внимание: **не** вешаем событие на `UpdateSettledBaseAndFee` — она может быть вызвана
повторно (`topUpSettledBase` ретраит) и приходит асинхронно. Вместо этого воркер при обработке
`order_paid` сам решает, готов ли заказ к постингу (см. «созревание» ниже).

### `order_refund`

Точка: **`RefundOrder`** (`internal/store/order/lifecycle.go:235` — именно lifecycle.go;
в refund.go только хелпер `refundAmountFromItems`, refund.go:18). Метод уже работает внутри
`s.txFunc(ctx, func(ctx, rep) { ... })`. Вставка — последней строкой замыкания, **после**
`updateOrderStatusAndAccumulateRefundedAmount(...)` (обернуть его `if err := ...; err != nil
{ return err }` вместо текущего `return ...`). К этому моменту в scope уже есть всё нужное
(проверено по телу метода):

- `order` — `getOrderByUUIDForUpdate` (есть `.UUID`, `.Currency`, `.ShippingRefunded`);
- `refundedAmount` — итоговая сумма ЭТОГО рефанда в валюте заказа, **уже включая** доставку,
  если она была добавлена (блок `if refundShipping && !order.ShippingRefunded { ... }`);
- `refundedByItem map[int]int64` — order_item_id → возвращённое количество (ровно то, что
  ушло в `insertRefundedOrderItems`).

```go
if err := rep.Accounting().EnqueueEvent(ctx, entity.AcctEventInsert{
    EventType: entity.AcctEventOrderRefund,
    // один заказ может рефандиться несколько раз (partially_refunded) — ключ должен
    // различать события: uuid + порядковый номер = COUNT(*) существующих refund-событий
    // этого заказа в acct_event + 1 (SELECT в той же Tx; операция редкая).
    SourceKey: fmt.Sprintf("%s:%d", order.UUID, seq),
    Payload: entity.AcctOrderRefundPayload{
        OrderUUID:      order.UUID,
        RefundAmount:   refundedAmount,      // в валюте заказа, ровно этот рефанд, с доставкой
        OrderCurrency:  order.Currency,
        RefundedByItem: refundedByItem,      // для COGS-части: qty по каждой позиции
    },
    OccurredAt: s.Now(),
}); err != nil {
    return fmt.Errorf("enqueue acct refund event: %w", err)
}
```

(JSON-маршалинг payload — внутри `EnqueueEvent`, продьюсеры передают типизированную структуру;
см. сигнатуру в 02.)

(`RefundShipping`-флага в payload нет — доставка уже внутри `refundedAmount`; отдельно её
выделять для проводки не нужно, VAT-доля считается от общей суммы.)

Важно: Stripe-refund происходит ДО store-вызова (`apisrv/admin/order.go:205,277→286`), поэтому
если store-Tx закоммитилась — деньги ушли и событие консистентно. Если Stripe упал — store не
вызывается, события нет. Ровно та же семантика, что у самих данных рефанда.

### Fail-safety продьюсеров

Вставка outbox — часть бизнес-Tx: если она падает (что почти исключено — INSERT в маленькую
таблицу), падает вся Tx. Это осознанный выбор: лучше откатить рефанд, чем получить рефанд без
следа в бухгалтерии. Для `order_paid` риск оценён приемлемым по той же причине (и таблица
локальная, без FK наружу). Никакой сетевой/внешней работы в этих точках не добавляется.

## 3.2 Созревание `order_paid` (ожидание settled base)

`total_settled_base`/`payment_fee` пишутся не в момент confirmed, а при
`capturePaymentDetails`/`topUpSettledBase` (задержка секунды—минуты, ретраи ~4 мин). Воркер при
обработке `order_paid`:

1. Читает «order facts» **отдельным SQL в accounting-сторе** (не `GetOrderFullByUUID`!):
   у entity `OrderItem`/`OrderItemInsert` **нет** поля `cost_price_at_sale` — колонка пишется
   raw-мапой в `insertOrderItems` (insert.go:167,173) и читается только прямыми SQL в metrics.
   Accounting-стор делает так же (прецедент — все файлы `internal/store/metrics/*`). Точный
   запрос — в `09-implementation-notes.md`. Поля: `total_settled_base`, `payment_fee`,
   `total_price`, `vat_amount`, `currency`, `payment.payment_method_id` (имя метода — через
   `cache.GetPaymentMethodById`), `shipment.cost` + `free_shipping`, по items:
   `COALESCE(oi.cost_price_at_sale, p.cost_price)` × `quantity` (fallback на живой
   `product.cost_price` — тот же, что в `metrics/margin.go:77`).
2. Решает по правилам готовности:
   - **`occurred_at` события < `accounting.start_date`** → skip: MarkEventProcessed +
     `last_error='pre-start event'`. Критично для прод-роллаута: продьюсеры деплоятся с
     `ACCOUNTING_ENABLED=false` и копят события ДО выбора cutover-даты — заказы до start_date
     в леджер попадать не должны (иначе выручка без материального контекста, «старт с нуля»
     нарушен). То же правило первым шагом для order_refund.
   - `total_settled_base != NULL` → постим (главный путь, Stripe).
   - метод оплаты не-Stripe (`cash`, `bank-invoice` — `entity.ValidPaymentMethodNames`) и
     `currency == EUR` → постим по `total_price` (точная EUR-сумма, settled не будет никогда).
   - метод не-Stripe и `currency != EUR` (USDT-кастомы и т.п.) → **skip с пометкой**: событие
     помечается processed + `last_error='non-eur non-stripe order, manual entry required'`;
     попадает в сверочный отчёт «требует ручной проводки». (`total_price_eur` НЕ используем —
     правило CLAUDE.md.)
   - Stripe, но settled ещё NULL → **отложить**: `MarkEventFailed("settled pending", 5m)` —
     событие получит `next_retry_at` и вернётся в выборку позже. После
     `accounting.settled_wait_max` (конфиг, дефолт 48h)
     не постим молча по фолбэку, а оставляем событие висеть и светим в health/сверке — significant
     gap означает проблему в capture-пайплайне, её надо видеть, а не маскировать.

Порядок внутри одного заказа — `order_refund` строго после `order_sale`:

   - Пропорция k в S2 требует того же G, что использовала S1. Поэтому правило: рефанд-событие
     обрабатывается **только если S1-проводка заказа уже существует**
     (`SELECT 1 FROM acct_journal_entry WHERE source_type='order_sale' AND source_key=:uuid`);
     иначе `MarkEventFailed("awaiting sale posting", 5m)`. Кейс реален: рефанд возможен из
     Confirmed сразу после оплаты, пока sale-событие ещё ждёт settled.
   - Рефанд заказа, который **никогда не будет запощен** (заказ оплачен до cutover'а, либо
     не-EUR не-Stripe): S1 нет и не появится → после `settled_wait_max` событие остаётся
     висеть с той же меткой → сверка показывает его в блоке Pending с причиной
     `pre-cutover order` / `manual entry required` (обе суммы разносятся вручную).

## 3.3 Pull-источники

Все pull-сканы живут в воркере (`07-worker-config.md`) и используют `acct_checkpoint` +
идемпотентность `CreateJournalEntry`. Чекпоинт двигается только после успешного постинга батча.

### `material_stock_movement` → checkpoint `material_movement` (по `id`)

```sql
SELECT m.*, mat.name AS material_name
FROM material_stock_movement m
JOIN material ON ...
WHERE m.id > :last_id
  AND m.created_at >= :accounting_start_date
ORDER BY m.id
LIMIT :batch;   -- батч 200
```

Каждое движение → одна проводка, `source_key = movement id` (`04-posting-rules.md`, правила
M1–M7). Движения с `unit_cost_base IS NULL` там, где он нужен (receipt/issue/writeoff/adjustment
с деньгами): проводку не создаём, движение попадает в отчёт сверки как uncosted (запрос по
`id <= checkpoint AND unit_cost_base IS NULL` — состояние восстановимо, отдельного лога не надо).
Чекпоинт при этом двигаем: «пропущено осознанно» ≠ «не обработано».

Внимание: `unit_cost_base` у **receipt** может быть NULL в момент прихода и НЕ дозаполняется
(uncosted receipt — легальное состояние склада). Не ждём его; сверка покажет.

Дата проводки движения: `occurred_at` движения может быть **задним числом** (админ вносит
приход прошлой датой). Правило clamp: `entry.occurred_at = max(movement.occurred_at,
accounting.start_date)`; если исходная дата попала в уже закрытый период — clamp к первому
дню текущего открытого периода + caveat `backdated movement` (иначе движение застрянет об
`ErrAcctPeriodClosed` навсегда — для pull-источников, в отличие от outbox, «висеть и ждать
reopen» нельзя: за ним стоит очередь по id).

### `production_run` → без числового чекпоинта, скан по отсутствию проводки

```sql
SELECT r.id FROM production_run r
WHERE r.received_at IS NOT NULL
  AND r.received_at >= :accounting_start_date
  AND NOT EXISTS (SELECT 1 FROM acct_journal_entry e
                  WHERE e.source_type = 'production_receive'
                    AND e.source_key  = CAST(r.id AS CHAR))
LIMIT :batch;
```

(идемпотентность и есть «чекпоинт»; ранов мало, скан копеечный). Правило P1 в
`04-posting-rules.md` — включая постинг ручных статей `production_run_cost` в WIP этим же
моментом.

### `opex_line` → checkpoint `opex_line` (по `updated_at`), repost месяца

opex_line мутируема (upsert по `(month, category, label)`, редактирование сумм, delete строки).
Стратегия: гранула постинга = **месяц**; при любом изменении месяца — пересборка его проводки.

1. `SELECT DISTINCT month FROM opex_line WHERE updated_at > :last_ts AND month >= :start_month`
   — месяцы до cutover'а игнорируются, даже если их строки редактируют (их расходы не в
   леджере by design «старт с нуля»). (+ месяцы, у которых строка удалена — ловится
   сравнением: entry существует, а Σ строк изменилась; см. reconcile.)
2. Для каждого месяца в одной Tx:
   - если период закрыт → пропустить + warning в лог/health (изменение OPEX закрытого месяца —
     нештатная ситуация, чинится reopen'ом периода);
   - существующая проводка `('opex_month','YYYY-MM')` есть и суммы совпадают → no-op;
   - есть и не совпадают → `ReverseJournalEntry` старой + `CreateJournalEntry` новой с
     `source_key='YYYY-MM:v2'` (версия = счётчик сторно+1);
   - нет → создать (правило O1).
3. `SetCheckpoint(last_ts = max(updated_at) батча)`.

Строки с `amount_base IS NULL` (нет FX на месяц) — пропускаются с caveat-флагом на entry,
консистентно с тем, как их пропускают метрики.

### Что сознательно НЕ является источником (фаза 1)

- `tech_card_dev_expense` (R&D-расходы стилей) — пересекается с issue_sample (известный caveat
  в StyleEconomics), решим маппинг в фазе 2.
- `product_stock_change_history` (сток готовой продукции) — FG в леджере двигают только
  order_sale/refund (COGS) и production_receive; ручные корректировки FG-стока в деньгах
  видны через сверку с `GetInventoryValuation`, правило для них добавим фазой 2 (нужен
  cost-контекст изменения).
- `shipment.actual_cost` / `return_shipping_cost` (фактическая себестоимость доставки) —
  расход перевозчика; фаза 2 (счёт 6030), т.к. проставляется вручную с задержкой и мутируемо.

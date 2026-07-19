# Часть 6: отчёты — Trial Balance, P&L, Balance Sheet, drill-down, сверка

Все отчёты — read-only агрегации по `acct_journal_line`/`acct_journal_entry`/`acct_account`
(`internal/store/accounting/reports.go`). Кэшировать нечего (объёмы малы, admin-only трафик);
`internal/cache` не трогаем (он для словарей).

Базовый строительный блок — CTE «обороты по счетам за интервал»:

```sql
WITH turnover AS (
  SELECT a.id, a.code, a.name, a.section, a.statement,
         SUM(CASE WHEN l.side='debit'  THEN l.amount ELSE 0 END) AS dr,
         SUM(CASE WHEN l.side='credit' THEN l.amount ELSE 0 END) AS cr
  FROM acct_account a
  LEFT JOIN acct_journal_line  l ON l.account_id = a.id
  LEFT JOIN acct_journal_entry e ON e.id = l.entry_id
        AND e.occurred_at >= :from AND e.occurred_at < :to
  GROUP BY a.id
)
```

Знак сальдо: `asset|cogs|opex` → `dr − cr`; `liability|equity|revenue` → `cr − dr`
(функция в Go по `section`, не в SQL — используется всеми отчётами одинаково).

## Trial Balance — `GetTrialBalance(from, to)`

CTE как есть → строки `TrialBalanceRow{code, name, section, dr, cr, balance}` + тоталы.
Инвариант ΣDr == ΣCr отдаётся полем `balanced bool` — UI красит красным при нарушении
(теоретически невозможно, практически — главный smoke-инвариант системы).

## P&L — `GetProfitLoss(from, to)` (Excel «Income Statement»)

Гранулярность — месяцы интервала (как колонки Mar-2026…Mar-2027 в Excel). SQL — тот же CTE с
дополнительной группировкой по `DATE_FORMAT(e.occurred_at, '%Y-%m-01')`, только `statement='PL'`.

Структура ответа (entity → pb):

```go
type AcctProfitLoss struct {
    Months   []time.Time                 // колонки
    Sections []AcctPLSection             // revenue / cogs / opex — в этом порядке
    // производные строки:
    TotalRevenue, NetCogs, GrossProfit, GrossMarginPct,
    TotalOpex, OperatingProfit, NetMarginPct []decimal.Decimal // по месяцам + YTD
}
type AcctPLSection struct {
    Section string
    Rows    []AcctPLRow                  // счёт → значения по месяцам + total
}
```

Правила знаков в P&L: revenue-счета показываются положительными (cr−dr), контра-счёт 4040 —
отрицательным (его баланс дебетовый → cr−dr < 0 — само сложится, отдельной логики не надо);
cogs/opex — положительными (dr−cr), контра 5050 — отрицательным. Итоги: `GrossProfit =
TotalRevenue − NetCogs` (5010+5040+5090+6210−5050... — просто Σ секции cogs),
`OperatingProfit = GrossProfit − TotalOpex`.

Отдельно поле `caveats []string` на ответе: количество проводок периода с `has_caveat` +
агрегат «непокрытых» операций из сверки (см. ниже) — P&L честно говорит, чего в нём нет.
Два **постоянных** caveat'а фазы 1 (выводятся всегда, зашиты в хендлер): «прибыль до налога
на прибыль (CT не начисляется)» и «расходы доставки перевозчику не учтены (доход 4110 без
расходной пары 6030 — фаза 2)». Это осознанные дыры P&L, они должны быть видимы, а не
подразумеваемы.

## Balance Sheet — `GetBalanceSheet(asOf)` (Excel «Balance Sheet»)

Обороты от начала времён до `asOf` включительно (`e.occurred_at <= :asOf`), `statement='BS'` +
виртуальная строка **Current Period Net Profit** = сальдо всех PL-счетов за тот же интервал
(нераспределённая прибыль; Excel-строка NP). Структура:

```
ASSETS  (section=asset, по коду)            → Total Assets
LIABILITIES (liability)                     → Total Liabilities
EQUITY (equity) + NP-строка                 → Total Equity
CHK: Assets − (Liabilities + Equity) == 0   → поле balance_check + bool
```

`balance_check` ≠ 0 невозможен при инварианте Dr=Cr, но поле обязательно (Excel CHK) — это
приборная панель доверия к системе.

## Drill-down — `GetAccountLedger(code, filter)`

Постраничный список строк по счёту с бегущим сальдо:

```sql
SELECT e.id, e.occurred_at, e.description, e.source_type, e.source_key,
       l.side, l.amount, l.note
FROM acct_journal_line l
JOIN acct_journal_entry e ON e.id = l.entry_id
WHERE l.account_id = :acc AND e.occurred_at >= :from AND e.occurred_at < :to
ORDER BY e.occurred_at, e.id, l.id
LIMIT :limit OFFSET :offset;
```

+ `opening_balance` (сальдо до `from`) одним запросом — бегущий баланс считает Go-цикл.
`source_type`+`source_key` в ответе — фронт делает ссылку на заказ/партию/движение.

## Сверка — `GetReconciliation(from, to)` (`reconcile.go`)

Смысл: леджер производен — сверка доказывает, что он не разошёлся с операционной правдой, и
показывает, что осознанно не запощено. Блоки ответа:

1. **Revenue**: `Σ NET+SHIP по order_sale-проводкам` vs
   `Σ total_settled_base − VAT-доля` прямым запросом по `customer_order` за период (плюс
   не-Stripe EUR-заказы). Дельта + список заказов без проводки (событие висит/skip).
2. **Fees**: Σ 6050 vs Σ `payment_fee`.
3. **COGS**: Σ 5010 vs Σ `cost_price_at_sale×quantity` по confirmed-заказам периода.
4. **Materials (1110)**: сальдо 1110 vs `GetInventoryValuation().RawMaterialsValue` (as-of
   вариант: Σ costed-движений напрямую) + счётчик uncosted-движений, пропущенных постингом.
5. **FG (1130)**: сальдо 1130 vs `TotalStockValue` — с оговоркой, что расхождение штатно
   (cost_price мутирует, ручные стоки), список причин по типам.
6. **Pending**: события outbox неразобранные (по возрасту и причине из last_error:
   `settled pending` / `awaiting sale posting` / `manual entry required` / `pre-cutover
   order`), received-раны без проводки, opex-месяцы без проводки.
7. **Unposted movements**: uncosted receipt/issue/writeoff за период (id, материал, qty).

Каждый блок: `ledger`, `operational`, `delta`, `items []` (ограниченный top-N + total count).
Это одновременно и админский отчёт, и источник для health-алертов воркера
(`07-worker-config.md`) — реализация одна.

## Тесты

`internal/store/accounting_reports_integration_test.go`: сценарий из `04-posting-rules.md`
«сквозной пример» прогоняется целиком (создать заказ/движения фикстурами → воркерная логика
постинга (вызов билдеров напрямую) → отчёты): TB сбалансирован; P&L: revenue 203.25, COGS
84.50, fee 7.55; BS: 1030=0 (после MN-вывода), 1130 = 422.50−84.50, CHK==0; drill-down 1030
даёт 3 строки с правильным бегущим сальдо; сверка revenue-дельта == 0.

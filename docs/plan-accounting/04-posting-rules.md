# Часть 4: правила проводок (Dr/Cr) — полная спецификация

## Учётная политика (явно, чтобы не спорить на ревью)

- **Момент признания выручки и COGS — подтверждение оплаты (confirmed)**, не отгрузка и не
  доставка. Причины: только в этот момент существует авторитетная EUR-сумма
  (`total_settled_base`); возвраты корректно разматываются через 4040/5050. Излишняя
  консервативность (признание при delivered, счёт 1140 Inventory in Transit из Excel) — фаза 2,
  если потребует бухгалтер: перенос точки признания = смена продьюсера события, правила не
  меняются.
- **P&L фазы 1 — до налога на прибыль** (UK CT — ручная проводка бухгалтера или фаза 2) и
  **без фактических расходов доставки** (4110 Shipping Income есть, 6030 расходов перевозчика
  нет до фазы 2 — маржа систематически завышена на стоимость доставки; отражено в caveats
  P&L, см. 06).
- Всё в EUR; строки > 0; знак несёт сторона; сторно вместо правок.

Чистая логика в пакете `internal/accounting` (без SQL — на вход факты, на выход
`entity.AcctJournalEntryInsert`; юнит-тестируется без БД):

```
internal/accounting/
  accounts.go   — константы кодов счетов + маппинг opex-категорий
  ordersale.go  — BuildOrderSaleEntry(order facts) (entry, error)
  refund.go     — BuildOrderRefundEntry(...)
  material.go   — BuildMaterialMovementEntry(movement, materialName)
  production.go — BuildProductionReceiveEntry(run, costs, ledgerWIP)
  opex.go       — BuildOpexMonthEntry(month, lines)
```

Все суммы EUR, `decimal.Decimal`, округление `.Round(2)` только на финальных строках
(промежуточные доли не округляем — иначе Dr≠Cr на копейку). Последняя строка проводки всегда
вычисляется как балансирующая разность, а не независимым умножением — гарантия Σdebit==Σcredit
при любых пропорциях.

## S1. Оплата заказа — `order_sale`, source_key = order uuid

Входные факты (см. «созревание» в 03): `G` — валовая EUR-сумма (=`total_settled_base`, либо
`total_price` для не-Stripe EUR-заказов), `F` = `payment_fee` (NULL→0; для не-Stripe методов —
модель `payment_method.fee_pct/fee_fixed`, как в метриках contribution), `total_price` (валюта
заказа), `vat_amount` (валюта заказа, inclusive), `ship` = `shipment.cost` если
`free_shipping=false`, иначе 0 (валюта заказа), метод оплаты, items.

Производные (пропорцией через settled — та же EUR-доля у каждой компоненты):

```
k    = G / total_price                       — курс «валюта заказа → фактические EUR»
VAT  = round2(vat_amount × k)                — NULL vat_amount → 0
SHIP = round2(ship × k)
NET  = G − VAT − SHIP                        — балансирующая величина (не округляется отдельно)
COGS = Σ по items: COALESCE(cost_price_at_sale, product.cost_price) × quantity
       (только строки, где COALESCE непустой — политика идентична metrics/margin.go:
        снапшот приоритетен, живой cost_price — fallback для строк до 0093)
```

Счёт выручки: метод `cash` → `4010`, иначе → `4020`.
Счёт денег (mAcc) по методу: Stripe (`card`/`card-test`) → `1030 Payment Processor`;
`cash` → `1010 Cash – Bank`; **`bank-invoice` → `1040 Accounts Receivable`** — кастомный заказ
рождается Confirmed, но инвойс ещё не оплачен: признаём выручку против дебиторки, а не
против денег. Поступление оплаты инвойса — ручная проводка Dr 1010 / Cr 1040 (см. MN).

| Строка | Dr | Cr | Сумма |
|---|---|---|---|
| деньги/дебиторка | mAcc (1030/1010/1040) | | G |
| выручка net | | 4020/4010 | NET |
| shipping income | | 4110 | SHIP (строка опускается при 0) |
| VAT (inclusive) | | 2070 | VAT (опускается при 0) |
| комиссия эквайринга | 6050 | | F (опускается при 0) |
| — против денег | | 1030 | F |
| COGS | 5010 | | COGS (опускается при 0) |
| — списание FG | | 1130 | COGS |

Caveats (has_caveat + текст): есть items без `cost_price_at_sale` (COGS занижен — перечислить
product_id); `vat_amount IS NULL` (VAT не выделен); fee по модели метода, а не факту.

**Гварды вырожденных сумм** (порядок применения в `BuildOrderSaleEntry`, детерминированно):

1. `total_price <= 0` или `G <= 0` → проводка не строится, событие skip с пометкой
   `degenerate amounts` (сверка покажет). На практике невозможно (custom-флоу требует
   positive total), но деление на ноль в k должно быть исключено кодом.
2. `VAT >= G` (кривой снапшот) → VAT-строку не выделять: VAT=0, caveat `vat exceeds settled`.
3. После VAT: `SHIP >= G − VAT` → SHIP-строку не выделять: SHIP=0, caveat.
4. Остаток `NET = G − VAT − SHIP` теперь строго > 0 — все строки валидны (`amount > 0`).
5. `F <= 0` → строк fee нет (уже в таблице).

То же каскадно в S2: `VATr >= R` → VATr=0 + caveat; `R <= 0` → skip.

Тест-инвариант: `G + F + COGS == NET + SHIP + VAT + F + COGS` → Σdebit == Σcredit всегда,
плюс table-driven тесты на каждый гвард.

## S2. Рефанд — `order_refund`, source_key = uuid:seq

Входные: payload (`RefundAmount` R_ord в валюте заказа — уже включает доставку, если она
рефандилась; `RefundedByItem` map order_item_id→qty) + заказ (G, total_price, vat_amount,
currency) + по каждому item из map: `COALESCE(oi.cost_price_at_sale, p.cost_price)` (тот же
fallback, что в S1/metrics).

```
k     = G / total_price
R     = round2(R_ord × k)                    — EUR-сумма рефанда
VATr  = round2(vat_amount × (R_ord / total_price) × k)   — VAT-доля рефанда
NETr  = R − VATr
COGSr = Σ cost_price_at_sale × qty возвращённых costed-позиций
```

| Строка | Dr | Cr | Сумма |
|---|---|---|---|
| сторно выручки | 4040 Returns & Refunds | | NETr |
| возврат VAT | 2070 | | VATr (опускается при 0) |
| деньги назад | | mAcc — тот же счёт, что дебетовала S1 (1030/1010/1040) | R |
| возврат на склад | 1130 | | COGSr (опускается при 0) |
| — контра-COGS | | 5050 Returns to Inventory | COGSr |

Примечания: `payment_fee` не сторнируется (Stripe fee при рефанде не возвращается — колонка
задокументирована как «полная, не уменьшается»); 4040 — контра-выручка (section revenue,
нормальное сальдо дебетовое — в P&L строкой «Less: Returns»); возврат на склад постим потому,
что `RefundOrder` реально делает `RestoreStockForProductSizes` — леджер зеркалит факт.

Предусловия S2 (проверяет воркер до вызова билдера):
- **S1-проводка этого заказа существует** — иначе defer `awaiting sale posting` (см. 03):
  k обязан совпадать с использованным в S1.
- Не-Stripe EUR-заказ: k=1 (G = total_price). Не-Stripe не-EUR: S1 не постился → рефанд тоже
  skip `manual entry required`.

## M1–M7. Движения материалов — `material_*`, source_key = movement id

`V = round2(quantity × unit_cost_base)` движения. Проводка не создаётся при `V == 0` или
NULL-компонентах (сверка покажет uncosted). occurred_at = `occurred_at` движения (fallback
`created_at`), description = имя материала + reason/comment.

| # | movement_type | Dr | Cr | source_type |
|---|---|---|---|---|
| M1 | `receipt` (закупка) | 1110 Materials | 2010 Accounts Payable | material_receipt |
| M2 | `receipt_production` (выход вспом. партии) | 1110 | 1120 WIP | material_receipt |
| M3 | `issue_production` | 1120 WIP | 1110 | material_issue |
| M4 | `issue_sample` | 6210 Samples & Prototyping | 1110 | material_issue |
| M5 | `return_production` | 1110 | 1120 | material_return |
| M6 | `return_sample` | 1110 | 6210 | material_return |
| M7 | `writeoff` | 5040 Inventory Write-offs | 1110 | material_writeoff |
| M8 | `adjustment` | Δ>0: 1110 / 5090 · Δ<0: 5090 / 1110 | | material_adjustment |

M1 кредитует **2010 AP как агрегат** (реестра поставщиков нет — `material.supplier` free-text).
Оплата поставщику — ручная проводка Dr 2010 / Cr 1010. Если по факту закупки платят сразу —
админ так же вручную гасит 2010; в фазе 2 можно добавить признак «оплачено сразу» на приход.

M8: `adjustment` в движении хранит qty и on_hand_before/after; знак дельты берём из
`on_hand_after − on_hand_before`; стоимость — по `unit_cost_base` строки (заморожен складом).

## P1. Receive производственной партии — `production_receive`, source_key = run id

К моменту receive в леджере на 1120 уже накоплены материальные issues партии (M3/M5). Чего в
леджере ещё нет — **ручные статьи** `production_run_cost` (CMT-пошив, логистика, duty...). Они
постятся этим же событием, и тем же событием WIP переводится в FG:

```
MANUAL   = Σ production_run_cost.amount_base                (строки с NULL base — в caveat)
LEDGER_WIP = Σ по movements этого run: issue_production×cost − return_production×cost,
             costed-only И ТОЛЬКО movements с created_at >= accounting.start_date —
             зеркало того, что реально запощено M3/M5. Ран, открытый до cutover'а, имеет
             issues, которых в леджере нет — включить их в перенос значило бы Cr 1120 на
             сумму, которая туда не дебетовалась (отрицательный WIP).
FG       = MANUAL + LEDGER_WIP
```

| Строка | Dr | Cr | Сумма |
|---|---|---|---|
| ручные статьи в WIP | 1120 | 2010 AP | MANUAL (опускается при 0) |
| партия готова | 1130 Finished Goods | 1120 | FG |

occurred_at = `received_at`. Caveats: uncosted issues (FG занижен — ровно те же условия, при
которых `ActualUnitCostBase` возвращает NullDecimal и cost_price не сидится); uncosted manual
cost строки; pre-cutover issues исключены (`pre-cutover WIP excluded`).

Сознательное расхождение с складом: `product.cost_price` сидится из `ActualUnitCostBase`
(целиком или никак), а леджер переносит costed-часть всегда — на балансе честная стоимость,
в caveat — что занижено. Сверка (06) сопоставляет 1130-ledger с `TotalStockValue` и объясняет
дельту uncosted-позициями.

## O1. OPEX месяца — `opex_month`, source_key = 'YYYY-MM' (+':vN' при repost)

По costed-строкам месяца (`amount_base NOT NULL`), сгруппированным по категории:

| Строка | Dr | Cr | Сумма |
|---|---|---|---|
| по каждой категории | 6xxx по маппингу | | Σ amount_base категории |
| итого начислено | | 2030 Accrued Expenses | Σ всех |

Маппинг категорий (`internal/accounting/accounts.go`; полный сет —
`entity.ValidOpexCategories`, `internal/entity/metrics.go:1323`):
`salaries→6330, rent→6340, software→6320, marketing_other→6110, production_content→6125,
taxes→6360, bank_fees→6060, professional_services→6350, logistics_office→6010, other→6390`.
Неизвестная категория (dto-валидация расширилась, маппинг забыли) → 6390 + caveat —
fail-open с видимостью, не потеря строки.

occurred_at = последний день месяца. Оплата этих расходов (банк) — ручной проводкой
Dr 2030 / Cr 1010, т.к. факт оплаты бекенду неизвестен.

Caveat: строки месяца с `amount_base IS NULL` пропущены (перечислить label'ы).

## MN. Ручные проводки — `manual`, source_key = 'manual:'+uuid

Произвольные сбалансированные строки из админки (05-admin-api.md). Типовые сценарии для
документации UI:

| Сценарий | Dr | Cr |
|---|---|---|
| Вывод со Stripe на банк | 1010 | 1030 |
| Оплата bank-invoice заказа поступила | 1010 | 1040 |
| Оплата поставщику | 2010 | 1010 |
| Оплата начисленных OPEX | 2030 | 1010 |
| Уплата VAT в налоговую | 2070 | 1010 |
| Chargeback/dispute Stripe (деньги изъяты) | 4040 (+6050 dispute fee) | 1030 |
| Взнос основателя | 1010 | 3010 |
| Дивиденды/draws | 3030 | 1010 |
| Opening balance (если решат внести) | 1010/1110/1130... | 3010 |
| Зарплата сверх OPEX-журнала | 6330 | 1010 |

Ручная проводка в валюте ≠ EUR: клиент вводит `amount_src`+`currency_src`, сервер конвертит в
EUR по `costing_fx_rate` на дату (`GetCostingFxRatesToBase` as-of), сохраняет и след, и
EUR-сумму. Нет курса → `InvalidArgument` (fail-closed, как `currency.ValidateMinimum`).

## Сквозной пример (модель из Excel-листа General Ledger, EUR)

Закупка ткани 180 → M1: Dr 1110 / Cr 2010 180.
Выдача в партию 180 + фурнитура 12.50 → M3 ×2: Dr 1120 / Cr 1110 192.50.
CMT 200 + overhead 30 внесены как production_run_cost.
Receive 5 шт → P1: Dr 1120 / Cr 2010 230; Dr 1130 / Cr 1120 422.50. Unit cost 84.50 → сидится
в `product.cost_price` существующим кодом (не леджером).
Продажа 1 шт за 250 (заказ EUR, VAT 23% inclusive, доставка 10, fee 7.55):
k=1; VAT=46.75(=250×23/123 из снапшота); SHIP=10; NET=193.25 →
Dr 1030 250 / Cr 4020 193.25, Cr 4110 10, Cr 2070 46.75; Dr 6050 7.55 / Cr 1030 7.55;
Dr 5010 84.50 / Cr 1130 84.50.
Вывод со Stripe: MN Dr 1010 242.45 / Cr 1030 242.45. Баланс 1030 = 0. ✔

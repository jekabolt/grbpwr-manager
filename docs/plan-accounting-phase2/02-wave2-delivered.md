# Волна 2: признание по доставке (delivered) + 1140 Inventory in Transit

Решение владельца: переходим. Cutover-политика — из мастер-плана (обе ветки сосуществуют,
история не переписывается). VAT-момент остаётся при оплате (подтвердить у бухгалтера).

## 2.1 Счета и схема

- Seed: `2090 Customer Prepayments` (liability), `1140 Inventory in Transit` (asset) — уже
  задуманы Excel'ем.
- Конфиг: `accounting.delivered_recognition_from` (DATE; пусто = старая политика — фича
  выключена независимо от деплоя).
- `acct_event`: типы `order_shipped`, `order_delivered`; `acct_journal_entry.source_type`:
  `order_prepayment`, `order_transit`, `order_delivered_sale` (имена финализировать на дипе).
  Расширение обоих CHECK — строго по идемпотентному паттерну 07 §7.2 (information_schema-guard
  + PREPARE, шаблон 0157); синхронно entity-константы + Valid*-мапы + drift-тесты + лейблы
  в клиентском constants.ts (07 §7.3).

## 2.2 Новые проводки (заказы с payment-датой ≥ cutover)

| Момент | Проводка |
|---|---|
| Оплата (S1n) | Dr mAcc G / Cr 2090 (G−VAT) / Cr 2070 VAT (режимный, волна 1); fee как раньше Dr 6050/Cr 1030. COGS НЕ постится |
| Отгрузка (shipped) | Dr 1140 / Cr 1130 на Σ cost_price_at_sale (сток уходит в транзит) |
| Delivered (S1d) | Dr 2090 / Cr 4020(4010/4310) NET + Cr 4110 SHIP; Dr 5010 COGS / Cr 1140 |
| Refund до delivered | Dr 2090 (+Dr 2070 VAT-часть) / Cr mAcc; если уже shipped — возврат стока Dr 1130 / Cr 1140 |
| Refund после delivered | S2 как сейчас (4040/2070/mAcc + 1130/5050) |

BS между оплатой и доставкой честно показывает обязательство (2090) и транзитный сток (1140) —
ровно семантика Excel-листа «Лист1» («нет на скл / есть деньги / есть обязательство»).

## 2.3 Точки захвата

- `order_delivered`: push-событие в замыкании `DeliverOrderWithSource`
  (`store/order/lifecycle.go:422` — ПРОВЕРЕНО: метод уже `s.txFunc(ctx, func(ctx, rep){...})`,
  `rep.Accounting().EnqueueEvent` после `transitioned=true`, по образцу OrderPaymentDone).
  Точка ЕДИНАЯ — все 4 пути сходятся в неё: admin `apisrv/admin/order.go:322`, AfterShip
  webhook `aftership/webhook.go:118`, deliverysync `worker.go:148`, авто-таймер
  `lifecycle.go:413`. Отдельных вставок не нужно.
- `shipped`: push-событие `order_shipped` в `SetTrackingNumber`
  (`store/order/lifecycle.go:142`; проверить на дипе волны, что тело — txFunc-замыкание с
  rep, как соседи). Push, не pull — консистентность с остальными продьюсерами.
- Воркер: ветвление по `payment-дата < cutover` — старый S1; иначе новая цепочка.
  События `order_shipped`/`order_delivered` для pre-cutover заказов — skip
  `pre-policy order` (MarkProcessed с меткой): их выручка уже признана старым S1.
- Скидочная раскладка 4030 (волна 3) живёт в ОБЩЕМ revenue-блоке билдера, который
  переиспользуют S1 и S1d — при любом порядке мерджа волн 2/3 логика одна.
- Семантика 1140 в этой волне — **outbound** transit (товар едет покупателю);
  inbound-транзит закупок не моделируем (материя приходуется при receive) — имя счёта
  Excel-совместимо, назначение зафиксировано комментарием seed'а. Refund
  определяет ветку по наличию S1-старой либо S1n-проводки (source_type различить:
  `order_sale` vs новые `order_prepayment`/`order_delivered_sale` — расширение CHECK
  source_type, +2 значения, drift-тесты).
- Custom-заказы (cash, born-Confirmed, доставка «в руки»): признание сразу — S1-старый
  формат остаётся для них навсегда (delivered-модель только для отгружаемых Stripe-заказов).

## 2.4 Крайние случаи (в дип-прогон волны)

Недоставленные заказы на конец месяца (2090-остаток — норм, BS-строка); shipped→pending_return
без delivered; отмена pre-cutover рефандов — уже покрыта S1-existence правилом; ledger-drill 2090
в UI (появится в Chart автоматически). Сверка: recon-блок revenue переучивается на
delivered-выручку + новый блок `prepayments` (2090 vs Σ оплаченных недоставленных).

## 2.5 DoD

Интеграционный сквозняк: оплата→S1n (2090), отгрузка→1140, delivered→выручка+COGS; рефанды
обеих веток; pre-cutover заказ идёт старой цепочкой; TB/BS сходятся; recon-блоки зелёные на
фикстурах. UI: без изменений экранов (новые счета/строки появляются сами), кроме
source-лейблов новых типов в constants.

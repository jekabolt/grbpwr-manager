# Волна 3: полный P&L — снимаем оба постоянных caveat'а (параллелится с волной 2)

Четыре независимых мини-фичи; после волны P&L перестаёт печатать «pre-tax» и «shipping cost
not booked» (хендлер снимает постоянные caveats — единственная точка пересечения с волной 2,
координировать merge).

## 3.1 Фактическая доставка → 6030 Shipping & Fulfillment (закрывает главный caveat)

- Seed: `6030 Shipping & Fulfillment` (opex).
- Источник: `shipment.actual_cost` / `return_shipping_cost` (EUR, проставляются вручную с
  задержкой, мутируемы — потому фаза 1 отложила). Модель: pull-скан как OPEX — чекпоинт по
  `shipment.updated_at` (ПРОВЕРЕНО: колонка есть, `0001:654`, `ON UPDATE CURRENT_TIMESTAMP`),
  гранула =
  shipment, `source_key = 'ship:'+shipment_id(+':vN')`, repost при изменении суммы
  (reverse+create, открытые периоды only): Dr 6030 / Cr 2030. occurred_at = shipping_date.
- Recon: блок shipping (6030 vs Σ actual_cost).

## 3.2 Dev-expenses → R&D в леджере

- `tech_card_dev_expense` (миграция 0101; entity `techcard.go:852`: kind
  `sample|materials|labour|outsourcing|other`, `AmountBase NullDecimal` — uncosted скип +
  caveat, стандарт фазы 1) — pull-источник: source_key `dev:'+id`, Dr 6210 / Cr 2030;
  append-only? — на дипе проверить Delete-RPC (DeleteTechCardDevExpense существует!) →
  удаление учтённой строки ловить сверкой либо сторно-repost'ом как OPEX (решить на дипе,
  склоняемся к repost-модели: source_key с версией). Известный caveat фазы 1 (пересечение
  с issue_sample-материалами в StyleEconomics) НЕ трогаем — в леджере это разные счета
  одной 6210-группы, дубля проводок нет (материалы шли из 1110, dev-expense — начисление);
  зафиксировать пояснением в 09-доке.

## 3.3 Discounts 4030 — строка «Less: Discounts»

- Seed: `4030 Discounts / Promotions` (revenue-контра).
- Постинг внутри S1/S1d: если `promo_discount_pct > 0` — валовая выручка раскладывается:
  Cr 4020 full-price NET-эквивалент, Dr 4030 скидка (реконструкция из promo_discount_pct;
  free-shipping-промо скидкой не считается). Итог P&L не меняется — появляется аналитика.
  Guard: только когда реконструкция сходится с G в пределах цента, иначе как раньше + caveat.

## 3.4 Налог на прибыль (CT)

- Seed: `2050 Income Tax Payable` (liability), `8010 Corporation Tax` — section `tax`
  (решено): расширение `chk_acct_account_section` по паттерну 07 §7.2 + section-знак в
  `sectionBalance` (reports.go:39: tax → дебетовый, как opex) + строка
  Net Profit after tax = OperatingProfit − Σ tax в P&L-тоталах (proto/dto/UI-строка).
- Постинг: ТОЛЬКО ручная MN-проводка бухгалтера (Dr 8010 / Cr 2050; оплата Dr 2050/Cr 1010) —
  автоначисления НЕТ (UK CT считает бухгалтер). Работа волны: счета + строка в P&L-тоталах +
  MN-шаблон в пресетах UI + снятие caveat'а «pre-tax» (заменить на условный: печатать, только
  если за период нет 8010-проводок).

## DoD

P&L за тестовый месяц: 6030-строка с суммой факт-доставки, 6210 пополнена dev-expense,
4030-строка при промо-заказе, tax-строка после MN; постоянных caveats нет (при заполненных
данных); repost 6030 при правке actual_cost работает; recon-блоки зелёные.

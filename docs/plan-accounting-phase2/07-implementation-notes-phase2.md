# Фаза 2, часть 7: справочник имплементатора — якоря, паттерны, решения

Агентам читать ПЕРЕД любой волной вместе с 00 и файлом волны. Всё проверено grep'ом
(июль 2026); разъехалось — доверять коду, поправить план.

## 7.1 Проверенные якоря кода (сквозная таблица)

| Волна | Что | Где |
|---|---|---|
| W1 | VAT-резолв сейчас | `internal/store/order/vat.go` `getVatRatePct(ctx, db, country)` — 2-буквенный код, нет в vat_rate → 0 |
| W1 | Вызов при создании заказа | `internal/store/order/insert.go:356-368`: `shippingCountry = orderNew.ShippingAddress.Country` → снапшот `vat_rate_pct`/`vat_amount` (inclusive, `entity.VatFromInclusive`) |
| W1 | Страна для воркер-фактов | В `GetOrderFactsForPosting` страны НЕТ — добавить JOIN `buyer`→`address`(shipping): брать `country_code` (CHAR(2), 0053) с fallback на `country` |
| W1 | Страна отгрузки | `a.c.ShippingLabel.ShipFromAddress()` (`app/app.go:293`) → `entity.LabelAddress.CountryISO2` (`entity/shipment.go:283`) |
| W1 | EU-список | В коде ОТСУТСТВУЕТ — константа в `internal/accounting/vatregime.go` (27 стран, без GB) |
| W1 | Proto-точки | `ReceiveMaterialStockRequest` поля 1–8 (admin.proto:5481) → добавить `input_vat_amount = 9`, `input_vat_regime = 10`; `CreateCustomOrderRequest` поля 1–8 (:3158) → `buyer_vat_id = 9` |
| W2 | Единая delivered-точка | `DeliverOrderWithSource` (`store/order/lifecycle.go:422`) — внутри `s.txFunc(ctx, func(ctx, rep){...})`, `rep` доступен → EnqueueEvent туда же после `transitioned=true`. Все 4 пути сходятся: admin `order.go:322`, aftership `webhook.go:118`, deliverysync `worker.go:148`, авто `lifecycle.go:413` |
| W2 | Shipped-точка | `SetTrackingNumber` (`store/order/lifecycle.go:142`) |
| W3 | Чекпоинт-скан доставки | `shipment.updated_at TIMESTAMP ... ON UPDATE` существует (0001:654) — pull по нему валиден |
| W3 | Dev-expense схема | `tech_card_dev_expense` (0101): `kind` `sample\|materials\|labour\|outsourcing\|other`, `amount`+`currency`+`amount_base NullDecimal` (`entity/techcard.go:852`) — uncosted (`!AmountBase.Valid`) скипаются с caveat, как всё в фазе 1 |
| W4 | Webhook-switch | `internal/payment/stripe/webhook.go:86` — единственный `case stripe.EventTypePaymentIntentSucceeded`; dispute-cases добавляются рядом |
| W4 | Dispute→заказ | `dependency.Order.GetOrderByPaymentIntentId` (dependency.go:214) уже есть |
| W5 | GBP-курсы | fxsync тянет ECB, GBP в наборе (`fxsync/ecb_test.go`) — `costing_fx_rate` GBP-строки уже копятся |
| W5 | Turnover-хелперы | `store/accounting/reports.go`: `sectionBalance` :39, TB :65, PL-pivot :95+ — CF строится на них |
| W0 | Алерт-тест-образец | `internal/store/new_flow_alerts_integration_test.go` |

## 7.2 Паттерн расширения именованного CHECK (нужен W1/W2/W3/W5)

Волны расширяют enum-CHECK'и фазы 1 (`chk_acct_entry_source_type` +2 значения W2,
`chk_acct_event_type` +2 W2/W4, `chk_acct_account_section` +'tax' W3). MySQL DDL
неоткатываем → идемпотентность через **information_schema-guard + PREPARE** — канонический
паттерн репо, шаблон: `0157_material_cti.sql:20-28`:

```sql
SET @sql = IF((SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
   WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME='acct_journal_entry'
     AND CONSTRAINT_NAME='chk_acct_entry_source_type') > 0,
  'ALTER TABLE acct_journal_entry DROP CONSTRAINT chk_acct_entry_source_type', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;
-- затем условный ADD (guard: COUNT(*)=0) с НОВЫМ полным списком значений, ТО ЖЕ имя
```

migrationlint это пропускает (запрещены только auto-имена `_chk_<N>`); enum-drift-тесты
фазы 1 сами упадут, пока entity `Valid*`-мапы не расширены синхронно — это фича, не бага.

## 7.3 Обвязка каждой волны (одинаковая, не переспрашивать)

- Номер миграции: `ls internal/store/sql | sort | tail` непосредственно перед PR (фаза 2
  идёт волнами — номера разбирают друг у друга).
- Proto-изменения волны → мини-цикл зеркала: байт-копия в grbpwr-proto → push → новый SHA в
  `proto/contracts/mirror-git-ref.txt` → в клиенте бамп сабмодуля + `make proto` + коммит
  генерата. Изменения только аддитивные — `make check-proto-contracts` без правки baseline.
- Новые RPC → `internal/rbac/rbac.go` `methodRequirements` (rd/wr SectionAccounting),
  rbac-тест — чек-лист.
- Новые source_type/event_type → entity-константы + Valid*-мапы + drift-тест-функции
  (по три файла как в фазе 1) + лейблы в клиентском `accounting/utils/constants.ts`.
- Воркер: «правило локов» (читать на пуле, писать короткой Tx) — блокер ревью, как в фазе 1.
- Песочница агентов: бекенд — без Go (writing-only, сверка чтением), клиент — tsc/prettier
  работают (`./node_modules/.bin/tsc --noEmit`), git не трогать. Финализация волны — промптом
  по образцу `docs/plan-accounting/11-finalize-beta-prompt.md` (обновить под волну).

## 7.4 Решения, принятые заранее (агентам не переспрашивать)

Ссылки из волновых файлов вида «07 §7.4.N» указывают на пункт N этого списка.

1. **VatFacts.DestCountry**: `country_code` из shipping-адреса, fallback `country` (оба есть
   в address); пусто/некорректно → режим `export` + caveat `unknown destination` (fail-safe:
   0% VAT НЕ начисляем молча в EU — caveat всплывает в сверке).
2. **Режимная ставка vs снапшот**: постим по режимной; `|режимная − снапшот| > 1%` → caveat
  `vat snapshot mismatch` (не блокер).
3. **VIES-проверка VAT-ID** — вне скоупа (ручная, поле free-form с format-валидацией).
4. **Custom-заказы (cash/bank-invoice)** НЕ переходят на delivered — признание при создании
   (born-Confirmed), навсегда.
5. **VAT-момент при delivered-модели** — остаётся при оплате (Cr 2070 в prepayment-проводке).
   Если бухгалтер потребует иначе — меняется одна строка билдера S1n, зафиксировано.
6. **Dispute fee**: сумма из `stripe.Dispute.BalanceTransactions` (проверить поля SDK на
   дипе W4); нет данных → постим только сумму спора + caveat.
7. **Revolut CSV формат**: колонки берём из реального экспорта владельца на дипе W4 —
   в спеке парсер за интерфейсом `BankCsvParser` (второй банк = вторая реализация).
8. **USDT средняя себестоимость** — считается из 1050-проводок на лету (стейбл, дельты
   копеечные); отдельного лота-учёта нет.
9. **Year-close и P&L истории**: year_close-проводки исключаются из PL-секций отчёта
   фильтром `source_type != 'year_close'` — единственное место, где отчёт фильтрует тип.
10. **GBP-пересчёт** — на лету в отчёте (BS — курс на дату, PL — среднемесячный из
    costing_fx_rate), НЕ вторая валюта леджера.
11. **Discounts 4030**: только когда реконструкция из promo_discount_pct сходится с G в
    пределах цента; иначе поведение фазы 1 + caveat (никогда не ломаем баланс ради аналитики).
12. **Нарезка агентов по волнам**: W1 — Opus (resolver+постинг) ∥ Sonnet (proto/UI-таб/экспорты);
    W2 — один Opus (сквозная логика) + Sonnet-тесты; W3 — Sonnet (4 независимые мини-фичи,
    спека полная); W4 — Opus (инбокс+disputes) ∥ Sonnet (USDT-шаблоны+AP/AR-вью);
    W5 — Opus (CF+year-close) ∥ Sonnet (ratios+filing-UI).
13. **Порог OSS €10k НЕ моделируем** — OSS-регистрация активна (владелец подаёт), resolver
    безусловно oss для EU-B2C ≠ PL. Если регистрация когда-то отзовётся — правка одной ветки.
14. **Отсутствие ставки в vat_rate для oss-страны** — заказ НЕ постится (skip + алерт
    `vat rate missing`), а не постится с нулевой ставкой: неверная декларация хуже задержки.
15. **VAT-экспорты source-type-agnostic** — агрегируют по `vat_regime` + периоду оплаты из
    2070-строк любого source_type (переживают W2), рефанды неттятся со знаком минус.

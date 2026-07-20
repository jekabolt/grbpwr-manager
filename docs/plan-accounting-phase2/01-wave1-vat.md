# Волна 1: VAT-движок (режимы, input-VAT, экспорты) — ПЕРВАЯ, налоговые дедлайны

Цель: заменить фазово-первую пропорцию «vat_amount×k на 2070» полноценной классификацией по
режимам из Excel-листа VAT&Crypto и дать бухгалтеру готовые суммы для JPK_VAT (ежемесячно,
25-е) и OSS (квартально). Подача — руками из наших экспортов; KSeF вне скоупа.

## 1.1 VAT-resolver — чистая функция (переживёт волну 2)

`internal/accounting/vatregime.go`:

```go
type VatFacts struct {
    DestCountry   string // shipping address.country_code, fallback .country (решение 07 §7.4.1);
                         // в GetOrderFactsForPosting добавить JOIN buyer→address — сейчас страны в facts НЕТ
    OriginCountry string // cfg ShippingLabel.ShipFromAddress().CountryISO2 (app.go:293, entity/shipment.go:283);
                         // для cash-заказов — 'GB' (Excel-правило: popup из UK-стока)
    IsB2B         bool   // непустой BuyerVatID (см. 1.3)
    BuyerVatID    string
    PaymentMethod entity.PaymentMethodName
}
type VatRegime string // oss | pl_domestic | export | wdt | uk_stock_domestic | none
func ResolveVatRegime(f VatFacts) VatRegime
```

Таблица (из Excel, сценарии 1–8): EU-страна ≠ PL, B2C → `oss` (ставка страны — vat_rate уже
хранит); PL B2C → `pl_domestic` 23%; не-EU (вкл. UK при отгрузке из PL) B2C → `export` 0%;
EU B2B с VAT-ID → `wdt` 0% reverse charge; UK B2B → `export`; cash/UK-origin → `uk_stock_domestic`
20%. EU-список — константа в пакете (в коде его нет — проверено); пустая/битая DestCountry →
`export` + caveat `unknown destination` (07 §7.4.1). Юнит-тесты на все 8 сценариев Excel +
эти гварды.

Существующий снапшот-механизм (`getVatRatePct` в `store/order/vat.go`, вызов
`insert.go:356-368` от `orderNew.ShippingAddress.Country`) НЕ трогаем — он продолжает писать
`vat_rate_pct/vat_amount` как сверочный след; постинг переходит на режимную ставку
(правило дельты — 07 §7.4.2).

## 1.2 Схема и счета

Миграции (номера — next-free на момент старта волны):
- seed-добавка счетов: `2080 VAT Input (Recoverable)` (liability, контра), `4310 Sales – B2B /
  Wholesale` (revenue), `4050 Trade Discounts B2B` (revenue, контра; сидируем сразу, постинг —
  когда появятся B2B-скидки).
- `customer_order` + `vat_regime VARCHAR(24) NULL` (снапшот решения резолвера при оплате) +
  `buyer_vat_id VARCHAR(32) NULL`; CHECK именованный.
- `material_stock_movement` + `input_vat_amount DECIMAL(12,2) NULL`, `input_vat_regime
  VARCHAR(16) NULL` (`wnt|import|domestic_pl|domestic_uk`) — для purchase-VAT приходов.

## 1.3 Продажи: постинг по режиму

`BuildOrderSaleEntry` (и будущий delivered-вариант) принимает VatFacts+Regime:
- `oss`/`pl_domestic`/`uk_stock_domestic`: как сейчас Cr 2070, но VAT пересчитывается
  РЕЖИМНОЙ ставкой от NET (не пропорцией снапшота): rate из vat_rate по стране режима;
  снапшот `vat_amount` остаётся сверкой (дельта > 1% → caveat `vat snapshot mismatch`).
- `export`/`wdt`: VAT-строки нет; wdt дополнительно требует BuyerVatID — иначе fallback в
  oss + caveat `wdt without vat id`.
- B2B (`wdt`/UK B2B): выручка на `4310` вместо 4020.
- `vat_regime` пишется на заказ тем же воркер-тиком (UPDATE рядом с созданием entry — в Tx).
B2B-ввод: `CreateCustomOrderRequest` + `buyer_vat_id = 9` (admin.proto:3158, после
shipment_cost=8) + поле в admin-форме кастом-заказа; валидация формата EU VAT (префикс
страны + 8–12 знаков), VIES — вне скоупа (07 §7.4.3).

## 1.4 Закупки: input-VAT

`ReceiveMaterialStockRequest` (admin.proto:5481, поля 1–8) получает `input_vat_amount = 9`,
`input_vat_regime = 10` + два поля в UI-форме прихода. Миграция movement-колонок и
CHECK — по паттерну 07 §7.2. Постинг M1 расширяется:
- `domestic_pl`/`domestic_uk`: Dr 1110 NET + Dr 2080 VAT / Cr 2010 GROSS.
- `wnt`/`import` (Art.33a): нетто-нулевой self-charge — Dr 2080 / Cr 2070 на VAT-сумму,
  материал как сейчас Dr 1110 / Cr 2010 NET. (Customs duty — уже входит в цену прихода.)

## 1.5 Экспорты для подачи

Новые RPC (`rd(SectionAccounting)`), UI — новый таб `vat` в Reports:
- `GetVatReturnPL(month)`: JPK_VAT-агрегаты — output по режимам (pl_domestic, wnt
  self-charge, oss-сводка справочно), input 2080 по типам, net payable. Плоская таблица +
  copy-TSV (формат полей — согласовать с бухгалтером на дип-прогоне волны; полный XML JPK —
  фаза 3, сейчас цифры для ручного заполнения).
- `GetOssReturn(quarter)`: B2C-продажи по странам EU: страна, ставка, NET, VAT.
- **Источник обоих экспортов — SOURCE-TYPE-AGNOSTIC** (переживает волну 2): агрегировать по
  `customer_order.vat_regime` + период ОПЛАТЫ (VAT-момент = оплата, 07 §7.4.5), суммы VAT —
  из 2070-строк проводок заказа независимо от source_type (order_sale сейчас,
  order_prepayment после W2). **Рефанды НЕТТЯТСЯ**: S2/refund-проводки дебетуют 2070 —
  экспорт включает их со знаком минус в периоде рефанда (корректировки JPK/OSS).
- Сверка: блок `vat` в GetAcctReconciliation (2070-леджер vs Σ по заказам режимно).
- **Invoice-страница** (`/orders/:uuid/invoice` — существует, клиентский
  `order/invoice-page.tsx`): для wdt-заказов печатать buyer VAT-ID + надпись
  «Reverse charge — Art. 194 Directive 2006/112/EC» (требование Excel-сценария 5).

## 1.6 Открытые вопросы бухгалтеру (закрыть на дип-прогоне волны)

- Подтвердить активную OSS-регистрацию (владелец: подаём — значит порог €10k пройден; порог
  НЕ моделируем, 07 §7.4.13) и UK VAT-регистрацию (иначе uk_stock_domestic 20% неприменим —
  режим cash-продаж пересмотреть).
- Полнота vat_rate: seed 0094 содержит стартовый список — сверить, что есть ВСЕ EU-27 + GB
  (недостающая ставка в oss-режиме → заказ скипается с алертом, не постится по нулю —
  07 §7.4.14).

## 1.7 DoD волны

Юнит: resolver 8 сценариев + постинг-варианты. Интеграция: EU/PL/UK/B2B-заказы → правильные
счета и vat_regime; приход с WNT → нетто-ноль 2070/2080. UI: таб vat показывает месяц,
числа сходятся с ручным расчётом бухгалтера за последний поданный месяц (**критерий приёмки —
прогнать параллельно с реальной подачей**). Существующие S1-тесты обновлены (VAT от режима).

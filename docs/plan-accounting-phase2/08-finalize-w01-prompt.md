# Промпт агенту: финализация волн 0–1 (техдолг + VAT) и бета

Скопируй в локального Claude Code (нужны Go/buf/mockery/MySQL/git/DO). База — тот же процесс,
что docs/plan-accounting/11-finalize-beta-prompt.md (фаза 1); ниже только специфика волн.

---

Ты финализируешь волны 0–1 фазы 2 бухмодуля. Код написан агентами в рабочем дереве
(некоммичен), ветка grbpwr-manager — `feat/betaseed-accounting` (там же МОЙ staged betaseed-WIP:
cmd/seed.go, internal/betaseed/** — коммить отдельно от волновых изменений или спроси меня).
Прочитай docs/plan-accounting-phase2/{00,01,06,07}.md.

Фаза A — сборка/тесты (гейт):
1. `git status` — новые/изменённые файлы волн: internal/accounting/** (vatregime, ordersale,
   material, common, accounts + тесты), internal/entity/{accounting,order,inventory,metrics}.go,
   internal/store/accounting/** (vatreturn.go, orderfacts, reconcile, ledger), два теста
   internal/store/acct_alerts_* и *_vat_integration_test.go + правка reports-теста,
   internal/store/{order/insert.go,inventory/inventory.go}, internal/acctposting/**, app/app.go,
   internal/dependency/dependency.go (+мок правлен руками — регенерируй), migrations 0191/0192,
   proto/admin/admin/admin.proto, internal/dto/{order,inventory,accounting,accounting_vat}.go,
   internal/apisrv/admin/{metrics.go,accounting_vat.go}, internal/rbac/rbac.go, migrationlint.
2. `gofmt -l` → -w; `make build` (buf+mockery+swagger; мок Accounting регенерится — ручные
   правки перезапишутся, это ок); `go test ./internal/accounting/... ./internal/rbac/...
   ./internal/store/migrationlint/...`; интеграционные:
   `go test ./internal/store/ -run 'TestAcct|TestAccounting' -v` (двойной прогон миграций
   0191/0192 на локальной БД — идемпотентность information_schema-guard'ов).
3. `make lint`; `make check-proto-contracts` упадёт на mirror-дрейфе — это Фаза B.

Фаза B — контракт: копия admin.proto в ../grbpwr-proto (байт-в-байт) → push → SHA в
proto/contracts/mirror-git-ref.txt → check-proto-contracts зелёный.

Фаза C — данные/энвы беты:
1. **ОБЯЗАТЕЛЬНО: seed vat_rate дополнить** — `GB` (20.00) отсутствует в 0094! Плюс сверить
   полноту EU-27 (SELECT country_code FROM vat_rate) — недостающие страны добавить (миграцией
   0193 INSERT..WHERE NOT EXISTS или руками на бете+проде; миграция чище). Без GB все
   cash-заказы будут скипаться 'vat rate missing'.
2. Новых env НЕТ (OriginCountry идёт из существующего SHIPPING_LABEL-конфига — проверь, что
   ShipFromAddress().CountryISO2 непустой на бете, иначе resolver получит '' origin).
3. Deploy беты → смоук: EU-заказ (DE) → проводка с 2070 по 19% и vat_regime='oss' на заказе;
   PL-заказ → 23%; UK Stripe → export без VAT; cash custom → uk_stock_domestic 20% + 4010;
   custom c buyer_vat_id DE123456789 → wdt, выручка 4310, инвойс-надпись позже (UI);
   приход материала с input-VAT domestic_pl → 2080-строка; wnt → нетто-ноль 2070/2080;
   GET vat-return/oss-return через Swagger — числа осмысленны; recon-блок vat зелёный.
4. **Критерий приёмки волны**: параллельный прогон vat-return с реальной ручной подачей
   бухгалтера за последний месяц (01 §1.6).

Фаза D — открытые review-пункты (реши/спроси меня):
- costing:write-гейт на input_vat_amount в ReceiveMaterialStock (у unit_cost есть — добавить?)
- input-VAT не виден в read-path common.MaterialMovement (нужно proto/common правка — надо ли в UI?)
- клиент: удалить стаб src/components/managers/accounting/journal/components/segmented-field.tsx
  (агенту отказали в delete-праве)
- eslint-миграция клиента на flat config (W0 п.7 — требует npm install, не делалась)

Фаза E — клиентский VAT-UI (СЛЕДУЮЩИЙ заход после контракт-синка, не блокирует бекенд-бету):
таб `vat` в Reports (два отчёта + copy-TSV), поле buyer_vat_id в форме кастом-заказа,
input-VAT поля в форме прихода, reverse-charge надпись на invoice-page для wdt, recon-карточка
vat. Спека: docs/plan-accounting-phase2/01-wave1-vat.md §1.5 + UI-паттерны фазы 1.

Отчёт: коммиты, тесты, смоук-чеклист по пунктам, решения по Фазе D.

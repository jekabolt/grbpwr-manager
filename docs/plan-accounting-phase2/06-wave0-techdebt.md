# Волна 0: техдолг-мини (1–2 дня, запускается немедленно, не ждёт остальных)

Хвосты фазы 1 из отчётов агентов — мелкие, но копятся. Один PR бекенд + один PR клиент.

## Бекенд

1. `acct_posting_lag_hours` → pb: поле в `AlertSettings` (proto), `AlertThresholdsToPb/FromPb`
   (internal/dto/metrics.go), снять защитный fallback-комментарий в
   `GetAcctPostingLag` (metrics/dashboard.go) на нормальную семантику (значение из настроек,
   ≤0 → дефолт остаётся как guard).
2. Интеграционный тест трёх `acct_*`-алертов: по образцу `new_flow_alerts_integration_test.go`
   (`TestDashboardAlertQueries`-паттерн) — прямые вызовы `GetAcctModuleActive`,
   `GetAcctPostingLag`, `GetAcctManualEntryRequiredCount`, `GetAcctRevenueReconForMonth`
   на фикстурах (зависший event, manual-required, дрейф >1%).
3. Point-lookup S1 для воркера: метод `HasSaleEntry(ctx, orderUUID) (bool, error)` в
   `dependency.Accounting` (+store: SELECT 1 по (source_type='order_sale', source_key)) —
   заменить пагинированный скан в `acctposting/outbox.go`; mockery regen.
4. Recon-тест «дельта == 0» с операционной фикстурой: посеять заказ+cache в
   `accounting_reports_integration_test.go`, добить блок revenue до нулевой дельты.

## Клиент

5. Поле `acct posting lag (hours)` в `alert-settings-modal.tsx` (после п.1 + контракт-синк
   зеркала — мини-бамп сабмодуля по тому же флоу).
6. Поднять `SegmentedField` из journal/components в `ui/form/fields/segmented-field.tsx`
   (single-select парный к multi-select toggle-group-field), journal переимпортировать.
7. Починить eslint-конфиг репо под ESLint 9 (flat config `eslint.config.js`, миграция правил;
   известная несовместимость eslint-plugin-react — обновить плагин) — чтобы `yarn lint`
   снова работал и в песочнице, и в CI.

## DoD

Бекенд: `make build` + все тесты зелёные; порог редактируется из админки и переживает
Upsert других порогов. Клиент: `yarn build:check` + `yarn lint` зелёные; сегмент-контрол
в ките. Контракт: зеркало+сабмодуль синхронно забампаны одним циклом.

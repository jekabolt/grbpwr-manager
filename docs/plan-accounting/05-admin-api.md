# Часть 5: admin API — proto, dto, apisrv, RBAC

## Proto (`proto/admin/admin/admin.proto`)

Новые RPC в `service AdminService` + сообщения. Деньги — `google.type.Decimal` (строка), как во
всех metrics-сообщениях. HTTP-биндинги под `/api/admin/accounting/...`; помнить правило gateway:
литеральные пути объявлять ПОСЛЕ параметризованных `/{id}`.

```proto
// --- план счетов ---
rpc ListAcctAccounts(ListAcctAccountsRequest) returns (ListAcctAccountsResponse);        // GET  /api/admin/accounting/accounts
rpc CreateAcctAccount(CreateAcctAccountRequest) returns (CreateAcctAccountResponse);     // POST /api/admin/accounting/accounts
rpc UpdateAcctAccount(UpdateAcctAccountRequest) returns (UpdateAcctAccountResponse);     // POST .../accounts/update  (только name)
rpc ArchiveAcctAccount(ArchiveAcctAccountRequest) returns (ArchiveAcctAccountResponse);

// --- журнал ---
rpc CreateJournalEntry(CreateJournalEntryRequest) returns (CreateJournalEntryResponse);  // manual
rpc ReverseJournalEntry(ReverseJournalEntryRequest) returns (ReverseJournalEntryResponse);
rpc ListJournalEntries(ListJournalEntriesRequest) returns (ListJournalEntriesResponse);
rpc GetJournalEntry(GetJournalEntryRequest) returns (GetJournalEntryResponse);

// --- периоды ---
rpc ListAcctPeriods(ListAcctPeriodsRequest) returns (ListAcctPeriodsResponse);
rpc CloseAcctPeriod(CloseAcctPeriodRequest) returns (CloseAcctPeriodResponse);
rpc ReopenAcctPeriod(ReopenAcctPeriodRequest) returns (ReopenAcctPeriodResponse);

// --- отчёты ---
rpc GetTrialBalance(GetTrialBalanceRequest) returns (GetTrialBalanceResponse);
rpc GetProfitLossStatement(GetProfitLossStatementRequest) returns (GetProfitLossStatementResponse);
rpc GetBalanceSheet(GetBalanceSheetRequest) returns (GetBalanceSheetResponse);
rpc GetAccountLedger(GetAccountLedgerRequest) returns (GetAccountLedgerResponse);        // drill-down
rpc GetAcctReconciliation(GetAcctReconciliationRequest) returns (GetAcctReconciliationResponse);
```

Полный список пар сообщений (имплементатору осталось только набрать поля по образцам ниже):

| RPC | Request-поля | Response-поля |
|---|---|---|
| ListAcctAccounts | include_archived bool | accounts []AcctAccount |
| CreateAcctAccount | code, name, section, statement | account AcctAccount |
| UpdateAcctAccount | code, name | account AcctAccount |
| ArchiveAcctAccount | code, archived bool | — |
| CreateJournalEntry | occurred_at, description, lines []AcctJournalLineInput | entry AcctJournalEntry |
| ReverseJournalEntry | entry_id int32, reason string | entry AcctJournalEntry (сторно) |
| ListJournalEntries | from, to, account_code, source_type, limit, offset | entries []AcctJournalEntry (без lines), total int32 |
| GetJournalEntry | id int32 | entry AcctJournalEntry (с lines) |
| ListAcctPeriods | — | periods []AcctPeriod |
| CloseAcctPeriod | month 'YYYY-MM' | — / not_ready AcctPeriodNotReady |
| ReopenAcctPeriod | month | — |
| GetTrialBalance | from, to | rows []TrialBalanceRow, total_debit, total_credit, balanced bool |
| GetProfitLossStatement | from, to | months []string, sections []AcctPLSection, totals AcctPLTotals, caveats []string |
| GetBalanceSheet | as_of | sections (assets/liabilities/equity), net_profit_row, balance_check, balanced bool |
| GetAccountLedger | code, from, to, limit, offset | opening_balance, rows []AcctLedgerRow (running_balance), total int32 |
| GetAcctReconciliation | from, to | blocks []AcctReconBlock (name, ledger, operational, delta, items, total_items) |

Опорные сообщения:

**Конвенция дат — строки `YYYY-MM-DD`**, не `google.protobuf.Timestamp`: в repo есть оба
прецедента, но для чистых дат (occurred_at, from/to, month) строковый выбран как в OPEX-API
(`admin.proto:4613` — «any date in the target month, YYYY-MM-DD; normalised server-side») —
без таймзонных сюрпризов Timestamp'а. Парсинг строго `time.Parse("2006-01-02", ...)`.

```proto
message AcctJournalLineInput {
  string account_code = 1;
  bool   is_debit     = 2;
  google.type.Decimal amount = 3;          // EUR; либо…
  google.type.Decimal amount_src = 4;      // …сумма в src-валюте
  string currency_src = 5;                 // тогда сервер конвертит по costing_fx_rate
  string note = 6;
}
message CreateJournalEntryRequest {
  string occurred_at = 1;                  // 'YYYY-MM-DD'
  string description = 2;
  repeated AcctJournalLineInput lines = 3;
}
message AcctJournalEntry {
  int32 id = 1; string occurred_at = 2; string description = 3;
  string source_type = 4; string source_key = 5;
  int32 reversal_of = 6; int32 reversed_by = 7;
  string created_by = 8; bool has_caveat = 9; string caveat = 10;
  repeated AcctJournalLine lines = 11;
  google.type.Decimal total = 12;          // Σ дебетов (= Σ кредитов)
}
message ListJournalEntriesRequest {
  string from = 1; string to = 2;          // occurred_at диапазон
  string account_code = 3;                 // фильтр по счёту (через lines)
  string source_type = 4;
  int32 limit = 5; int32 offset = 6;
}
message GetTrialBalanceRequest { string from = 1; string to = 2; }
message TrialBalanceRow {
  string code = 1; string name = 2; string section = 3;
  google.type.Decimal debit = 4; google.type.Decimal credit = 5;
  google.type.Decimal balance = 6;         // знак по нормальной стороне секции
}
```

P&L/BS-сообщения — в `06-reports.md` (структура повторяет Excel: секции + месячные колонки).

## DTO (`internal/dto/accounting.go`)

Конвертеры `ConvertAcctEntryToPb`, `ConvertTrialBalanceToPb` и т.д. Деньги — переиспользовать
существующие хелперы (`pbDecimalFromDecimal` / `nullDecimalFromPb` / `requiredDecimalFromPb` из
`dto/techcard.go`; они package-private в dto — доступны). Валидация входа:
- `occurred_at` парсится строго `2006-01-02`, не в будущем более чем на 1 день;
- строки ≥ 2; либо `amount`, либо (`amount_src`+`currency_src`), не оба;
- `currency_src` — `currency.IsSupported || currency.IsExpenseCurrency` (USDT легален);
- баланс НЕ проверять в dto — этим владеет store (`ErrAcctUnbalanced` → InvalidArgument).

## apisrv (`internal/apisrv/admin/accounting.go`)

Один файл на домен, методы на `*admin.Server`. Паттерн (валидация → store → dto → ответ).
**Мутации журнала — только внутри `s.repo.Tx`**: `CreateJournalEntry` /
`ReverseJournalEntry` — многозаписные операции, store намеренно не открывает Tx сам (02):

```go
var id int
err := s.repo.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
    var err error
    id, _, err = rep.Accounting().CreateJournalEntry(ctx, ins)
    return err
})
```

Read-RPC и одиночные записи (`ArchiveAcctAccount` и т.п.) — без обёртки. Маппинг ошибок:

| store-ошибка | gRPC-код |
|---|---|
| ErrAcctUnbalanced, ErrAcctUnknownAccount, парс-ошибки | InvalidArgument |
| ErrAcctPeriodClosed, ErrAcctPeriodNotReady, ErrAcctAlreadyReversed, ErrAcctSystemAccount | FailedPrecondition |
| not found | NotFound |

`created_by` — username из контекста авторизации (как `admin_username` в inventory-хендлерах,
`internal/apisrv/admin/inventory.go` — переиспользовать тот же способ извлечения).

`CloseAcctPeriod` возвращает при отказе структурированное `period_not_ready` описание
(pending events count, unposted movements count, ...) — чтобы UI показывал чек-лист.

## RBAC (`internal/rbac/rbac.go`)

Новая секция:

```go
// SectionAccounting governs the double-entry ledger: chart of accounts, journal
// (incl. manual entries), period close and accounting reports. Reports expose the
// same confidential figures as SectionCosting, so grant together in practice.
SectionAccounting = "accounting"
```

- Добавить строку в `var catalog = []SectionInfo{...}` (`rbac.go:79`):
  `{SectionAccounting, "Accounting", "Double-entry ledger: chart of accounts, journal, period close, financial reports."}`.
- В `methodRequirements` (`rbac.go:137`, хелперы `rd()`/`wr()` :130-131): все read-RPC →
  `rd(SectionAccounting)`; `CreateJournalEntry`, `ReverseJournalEntry`, `CreateAcctAccount`,
  `UpdateAcctAccount`, `ArchiveAcctAccount`, `CloseAcctPeriod`, `ReopenAcctPeriod` →
  `wr(SectionAccounting)`.
- Тест полноты RBAC (существующий) упадёт, пока каждое новое RPC не замаплено — это чек-лист.
- Field-shaping как у costing НЕ нужен: вся секция и так конфиденциальна целиком; отчёты не
  отдаются ролям без `accounting:read` вовсе.

## Swagger / кодоген / контракты

`make proto` → `make internal/statics` (обновить embedded swagger) → `make build`. Всё
покрывается обычным `make build`; отдельного шага нет. Дополнительно в репо есть
`make check-proto-contracts` (`scripts/check-proto-contracts.sh`, baselines в
`proto/contracts/`) — проверка breaking changes: наши изменения чисто аддитивные (новые RPC и
message'ы), должна пройти без правок baseline'ов; если скрипт ругается — разбираться, а не
двигать baseline.

## Замечание для фронта админки (отдельный Vercel-репозиторий)

Контракт REST: `/api/admin/accounting/*`, JSON от grpc-gateway, `google.type.Decimal` сериализуется
объектом `{"value":"123.45"}`. Экранов минимум четыре: Chart of Accounts, Journal (список +
форма ручной проводки + сторно), Reports (TB/P&L/BS + drill-down по счёту), Periods (+ сверка).
Бекенд-план фронт не покрывает.

# Волна 4: денежная сторона — Revolut-инбокс, USDT, disputes, AP/AR

Четыре блока, деплоятся независимо (внутри волны можно параллелить агентов A/B/C).

## 4.1 Revolut CSV-инбокс (лист «Unsorted» становится живым)

- Таблицы: `acct_bank_txn` (id, source='revolut', external_id UNIQUE — Revolut transaction id
  (дедуп повторного импорта), booked_at, amount DECIMAL, currency, description, counterparty,
  state VARCHAR chk: `unmatched|matched|posted|ignored`, matched_entry_id FK NULL,
  suggested_account VARCHAR NULL, raw JSON).
- RPC: `ImportBankCsv` (upload текста CSV; парсер за интерфейсом `BankCsvParser` — 07 §7.4.7,
  Revolut-колонки с реального файла владельца на дипе), `ListBankTxns(state)`,
  `PostBankTxn(id, счёт/шаблон)` —
  создаёт MN-проводку (Dr/Cr по знаку: приход → Dr 1010 / Cr выбранный; расход наоборот) и
  state=posted, `IgnoreBankTxn(id, reason)`. Не-EUR транзакции (Revolut мультивалютный:
  GBP/PLN…) постятся ГОТОВОЙ src-механикой фазы 1 (`amount_src`+`currency_src` →
  конвертация costing_fx_rate внутри CreateJournalEntry) — ничего нового не строить.
- Авто-подсказки (suggested_account): правила по подстрокам counterparty/description
  (STRIPE→сверка с 1030-выводами: матчить к существующим MN payout'ам вместо новой проводки;
  налоговая→2070/2050; известные поставщики→2010) — таблица правил `acct_bank_rule`
  (pattern, account_code) с CRUD в UI. Марджинальный ML не нужен — подстроки хватит.
- UI: новый таб `inbox` в Journal-экране: таблица unmatched с кнопками post (модалка =
  усечённая ручная проводка с префиллом суммы/даты/счёта) / ignore; счётчик в section-header.
  Префилл ручной проводки из recon-item (UI-бэклог) — та же механика префилла, делается тут.
- Сверка 1010: recon-блок `bank` (сальдо 1010 vs Σ posted+matched Revolut-остаток).

## 4.2 USDT-кошелёк (FRS 105, упрощённо для стейбла)

- Seed: `1050 Crypto Wallet (USDT)` (asset), `7010 Crypto Salaries`, `7020 Crypto Suppliers`,
  `7030 Crypto FX Gain/Loss` (opex-группа «crypto», в P&L внутри OpEx — как Excel).
- Учёт по себестоимости в EUR: покупка USDT — MN Dr 1050 / Cr 1010 по фактическому курсу
  сделки; выплата — MN-шаблоны Dr 7010|7020 (EUR-эквивалент по costing_fx_rate на дату) /
  Cr 1050 (по средней себестоимости остатка) / дельта → 7030. Средняя себестоимость кошелька —
  считается на лету из 1050-проводок (стейбл: дельты копеечные).
- Работа волны: счета + 3 MN-шаблона с калькулятором в UI (ввод: количество USDT + курс;
  форма сама считает EUR-ноги, показывает средневзвешенную себестоимость остатка 1050) +
  FAQ-инструкция. Автоматизации по блокчейну нет (объёмы — единичные платежи).

## 4.3 Stripe disputes (chargebacks)

- Webhook: в switch `HandleStripeEvent` (`webhook.go:86` — сейчас единственный case
  PaymentIntentSucceeded) добавить cases `charge.dispute.created` / `.closed`: outbox-событие
  `order_dispute` (event_type — CHECK-расширение по 07 §7.2). Заказ по PI:
  `rep.Order().GetOrderByPaymentIntentId` (dependency.go:214 — существует). Fee — из
  `Dispute.BalanceTransactions` (поля SDK проверить на дипе; нет → только сумма + caveat,
  07 §7.4.6).
- Постинг: created → Dr 4040 сумма + Dr 6050 dispute fee / Cr 1030 (деньги изъяты);
  closed won → сторно. COGS не трогаем (товар не возвращён). Recon-блок pending учитывает.
- Alert `acct_dispute_open` на дашборд.

## 4.4 AP/AR-субледжеры (минимальный, без оверинжиниринга)

- `supplier` (id, name, vat_id NULL, notes) + `material_stock_movement.supplier_id FK NULL`
  (рядом с free-text supplier_doc; каталог материалов supplier free-text остаётся) +
  выбор поставщика в форме прихода.
- AP-вью (не новая таблица): `GetPayables` — открытые Cr 2010 по supplier_id (проводки M1
  получают supplier-мету в note/линк) минус оплаты (MN Dr 2010 с supplier-тегом — поле
  `counterparty_id` на acct_journal_line? Решение на дип-прогоне: проще добавить
  `supplier_id NULL` на acct_journal_entry). UI: таб `payables` в Reports: поставщик /
  начислено / оплачено / остаток.
- AR: bank-invoice кастомы уже на 1040 — вью `GetReceivables`: открытые инвойсы (1040-проводки
  без парной оплаты), кнопка «оплата поступила» = MN-шаблон Dr 1010 / Cr 1040 с префиллом.

## DoD волны

Импорт реального Revolut CSV → инбокс → post 3 транзакций по подсказкам → 1010 сходится;
USDT-зарплата через шаблон → 1050/7010/7030 корректны; тестовый dispute (Stripe CLI) →
проводка и алерт; приход с поставщиком → payables-вью показывает остаток, оплата гасит;
bank-invoice → receivables-вью → «оплата поступила» закрывает.

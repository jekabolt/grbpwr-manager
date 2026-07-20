# Дип-ревью аккаунтинг-кода: находки (4 адверсариальных среза + верификация)

Срезы: A — билдеры/VAT-математика; B — воркер/продьюсеры/идемпотентность; C — store/SQL/
отчёты/сверки; D — API/dto/валидации. Все CRITICAL/HIGH верифицированы интегратором чтением
указанных строк. Формат: [сев.] код-находки · файл:строка · суть · фикс.

## CRITICAL — расхождение денег в декларациях

**[C-1] A-1 · internal/accounting/refund.go:52-58 + acctposting/outbox.go:175 —
S2-рефанд считает VAT из СНАПШОТА, а S1 (W1) постит РЕЖИМНЫЙ VAT.**
`BuildOrderRefundEntry` не получает VatDecision вовсе; VATr = vat_amount×доля. Любой заказ,
где снапшот ≠ режим (cash/uk_stock: 23% vs 20%; wdt/export: снапшот>0 vs 0%; NULL-снапшот),
после рефанда оставляет фантомный остаток на 2070 навсегда и искажает 4040. Прямо
коррумпирует JPK/OSS (vatreturn суммирует 2070 order_sale+order_refund). Репро: cash 120 EUR
PL-адрес → S1 Cr 2070 20.00 (режим 20%), полный рефанд Dr 2070 22.44 (снапшот 23%) →
2070 = −2.44. Wdt-рефанд дебетует 2070 без парного кредита вообще.
Фикс: S2 зеркалит S1 — перерезолвить/передать VatDecision (facts.VatRegime уже снапшотится
воркером на заказ!) и `vatr = vatInclusive(r, режимная ставка)`.

## HIGH

**[H-1] B-1 · outbox.go:98-104,249-259 + periods.go:69-78 — терминальный skip
('vat rate missing', non-EUR, degenerate) = невидимая для ClosePeriod дыра выручки.**
Skip = Failed+Processed; событие исчезает из pending; появление ставки позже НИЧЕГО не
перепощивает (нет reprocess-пути); ClosePeriod проверяет только pending → месяц закрывается
с незапощенной выручкой и VAT. Уже кусалось: 0193 (GB) существует именно поэтому.
Фикс: 'vat rate missing' → defer (без Processed), как settled-pending; ClosePeriod +
completeness-гейт «каждый confirmed-заказ месяца имеет order_sale-проводку»; admin-RPC
reprocess/resolve события.

**[H-2] B-2 · outbox.go:154-163 + periods.go:71 — рефанд скипнутой продажи
(pre-cutover / non-EUR / vat-missing) вечно pending 'awaiting sale posting' →
soft-lock ClosePeriod месяца рефанда без пути разрешения.**
Фикс: ограничить defer (возраст/attempts → терминальная 'orphan refund, manual entry'
диспозиция, толерантная для close) + admin-resolve.

**[H-3] C-1 · store/accounting/reconcile.go:133 — reconRevenue не включает 4310.**
Каждая B2B-продажа даёт ложный минус-drift (ledger без 4310, operational с заказом).
OSS-return 4310 включает — recon забыл. Фикс: добавить "4310" (и 4050-контру при появлении).

**[H-4] C-2 · store/accounting/vatreturn.go:66-73,105-116 — NetPayable смешивает юрисдикции.**
(а) input `domestic_uk` суммируется в InputDomestic → занижает ПОЛЬСКИЙ NetPayable на
невозмещаемый в PL UK-VAT; (б) output `uk_stock_domestic` (2070-строки существуют!)
выброшен switch'ем молча. Сдаваемое число неверно при любом UK-приходе/продаже.
Фикс: UK-вход/выход — отдельные поля вне польского NetPayable (или отдельный UK-return);
не глотать неизвестные режимы в switch — default+caveat.

**[H-5] D-1 · proto admin.proto:5505 (unit_cost) — NET-семантика не задокументирована →
операторская ловушка двойного VAT.**
Брутто unit_cost + input_vat_amount = задвоенный VAT: раздутые склад/COGS И возмещение 2080 →
занижен NetPayable. Фикс: proto-комментарии «unit_cost = NET (VAT-exclusive)» на оба поля +
мягкий guard (input_vat ≤ net×max_rate) + подпись в UI-форме прихода.

## MED

- **A-2** ordersale.go:86-90 — fee не-Stripe методов кредитуется на 1030 вместо mAcc (1010/1040)
  → фантомный минус на 1030. Фикс: Cr moneyAccount().
- **A-3** production.go:53 vs material.go:28 — P1 суммирует НЕокруглённые issue-values, M3/M5
  постят округлённые → residual-копейки на 1120 навсегда. Фикс: per-issue Round(2) в P1.
- **A-4** refund.go:104-118 — refundCOGS не клампит qty к проданному и молча игнорирует
  item-ключи вне facts (недовозврат без caveat). Фикс: clamp + caveat.
- **B-3** pull.go:80-111 — movements-батч без per-entry изоляции: один poison-movement
  (не-period ошибка) клинит ВСЮ очередь движений и все будущие close. Фикс: skip+log+alert
  как в processRuns.
- **B-4** pull.go:180,205 + DATETIME(0) — секундная гранулярность opex-чекпоинта + строгий `>`
  теряет правку в суб-секундном окне тика → месяц навсегда не перепощен, recon не видит.
  Фикс: DATETIME(6) или checkpoint=scanStart−margin c `>=` (sameOpexLines дедуп уже есть).
- **B-5** outbox.go:206-217 — нет dead-letter: детерминированная ошибка ретраится вечно (6h cap)
  и блочит close. Фикс: N attempts → терминальная manual-review диспозиция + admin-resolve.
- **C-3** vatreturn — WDT/export NET-разделы JPK (K_21/K_22) отсутствуют полностью.
  Фикс: агрегаты net по vat_regime wdt/export отдельными полями.
- **C-4** reconcile.go:128-160 — revenue-drift после W1 ожидаем (ledger=режим,
  operational=снапшот), но док/пороги это не отражают → ложные алерты. Фикс: operational по
  режимной ставке или задокументировать+не алертить.
- **C-5** orderfacts.go:29-44 — header-JOIN payment/buyer/address без ORDER BY: при дублях
  строк недетерминированный метод оплаты/страна. Фикс: ORDER BY p.id + cardinality-guard.
- **D-2** apisrv accounting.go:214 — ClosePeriod не обёрнут в repo.Tx вопреки контракту store
  (TOCTOU гейтов). Фикс: обернуть.

## LOW (короткой строкой)

A-5 opex: caveat-only месяц теряет метки (ErrSkipEmpty) — убрать ложный doc-комментарий/сюрфейсить в recon ·
A-6 accounting.go:231 firstOfDayUTC — граница дня в UTC, локальная полночь съезжает месяцем (фикс-таймзона) ·
A-7 outbox.go:136-140 — ставка 0 в vat_rate = молчаливый 0% VAT (трактовать ≤0 как missing) ·
B-6 events.go — last_error не чистится при успехе после defer (misleading) ·
C-6 periods.go:91-107 — TIMESTAMP-сравнения без пина session time_zone к UTC в DSN ·
C-7 accounting.go:196-208 — dup-recovery SELECT под REPEATABLE READ может дать ErrNoRows (locking read) ·
C-8 orderfacts INNER JOIN product — латентно (только при будущем hard-delete) ·
C-9 reports.go:205-219 — caveat-счётчик P&L включает сторнированные исходные ·
D-3 dto:335-341 — FX-фолд не ревалидирует границы DECIMAL(12,2) → Internal вместо InvalidArgument ·
D-4 dto:225 — нет cap на lines[] (~9300 строк = плейсхолдер-лимит MySQL) ·
D-5 rbac:229 — GetVatRates/Upsert под analytics, а питает налоги (segregation-of-duties) ·
D-6 buyer_vat_id: внутренние пробелы не вычищаются ('DE 123…' отвергается).

## Проверено-ОК (важное, чтобы не перепроверять)

Продьюсеры атомарны (EnqueueEvent в бизнес-Tx, ошибки проброшены); settled single-write
инвариант — гонки нет (но: не перечитывается в Tx — сломается, если появится correction-путь);
refund seq сериализован FOR UPDATE; Tx-ретраи повторяют замыкание целиком; entry+Processed
одна Tx; чекпоинт пустого батча не двигается; clamp идемпотентен; runOnce не перекрывается;
EU-27 точен, PL-B2B→pl_domestic верно; движ-guard'ы M8/wnt-0; RBAC 18/18 + не в allowlist;
все dto-валидации входа (scale, лимиты, даты, both-or-nothing) строгие; mapAcctErr полон,
SQL-тексты не утекают; миграции 0191-0193 идемпотентны, CHECK==Valid-мапы; TB/PL/BS
интервалы/знаки/NP/running-balance корректны; OSS-неттинг и collation-CAST'ы верны.

## Порядок фикса (рекомендация)

1. **Пакет «налоги» (до следующей подачи!)**: C-1(S2-VAT) + H-4 + H-3 + C-3 + A-7 + H-5(док).
2. **Пакет «устойчивость close»**: H-1 + H-2 + B-5 (единый механизм: defer-vs-terminal
   диспозиции + completeness-гейт + admin reprocess/resolve) + D-2 + B-3.
3. **Пакет «копейки/гигиена»**: A-2, A-3, A-4, B-4, C-4, C-5 + LOW по ходу.

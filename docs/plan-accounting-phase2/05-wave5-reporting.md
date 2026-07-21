# Волна 5: отчётность и filing — CF, ratios, year-end, GBP/FRS 105 (к ноябрю 2026)

## 5.1 Cash Flow (indirect) — чистая производная леджера

- `GetCashFlowStatement(from,to)`: NP периода (PL-сальдо) + ΔBS-статей между from−1 и to:
  add-back 5080-амортизация (когда появится), (Δ1040+Δ2090) AR/prepayments, Δ2010+2030+2050+2070
  AP/accruals, Δ(1110+1120+1130+1140) inventory → Net Operating; Investing: Δ1210/1220/1230;
  Financing: Δ3010/3030 + Δ2060. Closing cash (1010+1030+1050) + cross-check с BS (Excel-лист
  Cash Flow один в один). Все дельты — переиспользуя turnover-запросы reports.go (фактическая
  реализация — INNER-join агрегаты + `sectionBalance` :39, не CTE; хелперы готовы) двумя датами.
- UI: таб `cash flow` в Reports (месячные колонки как P&L, copy-TSV).

## 5.2 Financial Health ratios поверх леджера

- `GetFinancialHealth(from,to)`: Excel-лист целиком — Profitability (GM%, NPM, ROA, ROE),
  Liquidity (current/quick ratio из BS-групп), Inventory (turnover, DIO, sell-through —
  units из операционки как в GetMetrics), Cost structure (COGS%, discount rate — 4030 после
  волны 3, return rate — 4040), Leverage (D/E), Fashion (revenue/SKU, GMROI). Каждая строка:
  value + benchmark-диапазон из Excel + статус ok/warn. Переиспользовать формулы GetMetrics
  где источники совпадают; расхождений двух истин не плодить — ledger-first, операционные
  units подписывать источником.
- UI: таб `health` (таблица ratio/formula/benchmark/value со статус-цветом).

## 5.3 Year-end close (обязательно до конца 2026)

- RPC `CloseFinancialYear(year)`: гейт — все 12 периодов закрыты; создаёт закрывающую
  проводку 31.12: все PL-сальдо → `3020 Retained Earnings` (source_type `year_close` —
  CHECK-расширение по 07 §7.2 + entity/drift + фильтр в PL-отчёте, решение 07 §7.4.9);
  reopen года — сторно (только super).
- **Ловушка «закрытый декабрь» (найдена пролётом)**: гейт требует все 12 периодов closed, но
  публичный `CreateJournalEntry` отвергает постинг в закрытый период → year_close-проводка
  создаётся ВНУТРЕННИМ store-методом `createYearCloseEntry` с осознанным bypass
  period-guard'а (комментарий: единственное легальное исключение; идемпотентность по
  source_key `year:2026` та же). Сторно reopen'а — тем же внутренним путём.
- BS после закрытия: NP-строка нового года начинается с нуля, прошлые года сидят в 3020
  (FAQ 19 фазы 1 закрывается). P&L исторических лет не меняется (PL-обороты остаются, отчёт
  фильтрует year_close-проводки из PL-секций — пометить в reports.go).
- Periods-UI: секция года со статусом + кнопка close year (двойное подтверждение).

## 5.4 GBP-отчётность + FRS 105 формы (filing pack)

- Курс: `costing_fx_rate` GBP уже сыплется fxsync'ом (ПРОВЕРЕНО: GBP в ECB-наборе,
  fxsync/ecb_test.go — история копится с включения fxsync). Политика пересчёта — стандарт
  FRS 105/закрытый курс: BS-статьи по курсу на дату отчёта, PL — по среднемесячным (средняя
  из дневных за месяц; хранится расчётно). НЕ вторая валюта леджера — пересчёт на лету в
  отчёте.
- **Балансировка GBP-BS (найдена пролётом)**: BS по closing rate + PL по средним даёт
  Assets ≠ L+E в GBP — обязательна расчётная балансирующая строка
  **`Currency translation difference`** в equity GBP-представления (= разность; в EUR-книгах
  её нет). Без неё CHK в GBP всегда красный. Тест DoD: GBP-BS balanced.
- `GetFilingPack(year)`: (a) IS в 7 прописанных строках FRS 105 §5.3 (маппинг счетов →
  строки: Turnover=4xxx−4040−4030(−4050); Cost of sales=5xxx; Gross profit; Administrative
  expenses=6xxx+7xxx; Operating profit; Tax=8010; Profit/Loss), (b) BS Format 1 §4.3
  (Fixed/Current assets, Creditors, Capital&Reserves), (c) шапка компании (название/рег№/адрес —
  новые конфиг-ключи `accounting.company_name/company_number/company_address` + bindEnvVars +
  .do-энвы, паттерн cfg.go фазы 1), micro-entity statement — всё в GBP и EUR парой.
- UI: таб `filing` (год → две таблицы + copy-TSV; печатная форма — print-роут ProtectedBare
  по образцу techCardPrint). Сюда же UI-бэклог: **CSV/print-экспорт** (генерализуем
  copy-TSV кнопкой download .csv на всех отчётах), GBP-переключатель (тумблер EUR/GBP на
  TB/P&L/BS — пересчёт бекендом, query-параметр currency), пресеты периодов (сохранение
  последнего выбранного диапазона в searchParams-ссылки — уже есть; «сохранённые пресеты»
  скипаем как избыточные, отметить).

## DoD волны

CF: net change == Δcash BS месяц-к-месяцу на бете; ratios сходятся с ручным расчётом по BS/PL;
year-close на тестовой копии года: BS сходится, 3020 = накопленный NP, reopen-сторно чист;
filing pack за 2026 отдан бухгалтеру на вычитку (приёмка — его подпись, что формы пригодны
для Companies House/HMRC-заполнения).

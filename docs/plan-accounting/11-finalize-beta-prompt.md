# Промпт агенту: финализация accounting-модуля и деплой на бету

Скопируй всё ниже в локального агента (Claude Code на маке — нужны git push, Go/buf/mockery,
yarn, MySQL, doctl/DO-доступ). Песочница этого не умеет — работа только локальная.

---

Ты финализируешь бухгалтерский модуль GRBPWR (бекенд + админ-UI) и выводишь его на бету.
Код полностью написан и проверен статически; твоя работа — сборка, тесты, коммиты, деплой,
смоук. Ты НЕ пишешь новую функциональность; при падениях чинишь минимальными диффами.

Репозитории:
- Бекенд: ~/go/src/github.com/jekabolt/grbpwr-products-manager (ветка feature/accounting-core;
  Шаг 1 закоммичен 0dc3510, шаги 2–8 — некоммиченные изменения в дереве)
- Зеркало контракта: ~/go/src/github.com/jekabolt/grbpwr-proto (или рядом; клиентский
  сабмодуль указывает на f64a6b72cd9497e9b3768d32a82aac060c66645b)
- Клиент: ~/go/src/github.com/jekabolt/grbpwr-admin-client (ветка feat/plm-cutover;
  некоммиченные: accounting-UI, бамп сабмодуля proto, перегенерённый
  src/api/proto-http/admin/index.ts)

Источники истины (прочитай ДО работы):
- бекенд: docs/plan-accounting/{00,08,09,10}.md (08 — rollout-чек-листы, 09 §9.5 acceptance,
  10 §Шаг 9)
- клиент: docs/plan-accounting-ui/{00,06,07}.md
- CLAUDE.md обоих репо (правила веток: feature → beta → master; НИКОГДА не пушить в master
  в этой задаче)

ЖЁСТКИЕ ПРАВИЛА: не трогать master и прод-энвы; не коммитить секреты (.env,
config/config.toml, .do/app-beta.yaml, *.bak — gitignored, проверяй git status перед каждым
коммитом; .claude/ не добавлять); ничего не править в src/api/proto-http/* руками — только
buf generate; при неожиданном состоянии (конфликты, чужой WIP, красные тесты, которые не
чинятся ≤30 мин) — остановись и спроси меня, не импровизируй.

## Фаза 0 — санитария git (бекенд)

1. `cd grbpwr-products-manager && rm -f .git/index.lock` (остался от песочницы).
2. `git status` — ожидаемо: ~14 M-файлов + новые internal/accounting/, internal/acctposting/,
   internal/store/accounting/, apisrv/dto accounting, 4 интеграционных теста, docs/plan-accounting/.
3. Коммиты (логическая нарезка, БЕЗ .claude/ и *.bak):
   - `accounting: store layer + entity facts (step 2)` — dependency.go, entity/accounting.go,
     store/accounting/, store.go, accounting_core_integration_test.go
   - `accounting: posting rule builders (step 3)` — internal/accounting/
   - `accounting: outbox producers in order flows (step 4)` — store/order/{payment,create,lifecycle}.go,
     acct_producers_integration_test.go
   - `accounting: posting worker + config + wiring (step 5)` — internal/acctposting/, config/cfg.go,
     app/app.go, .do/app.yaml, acctposting_integration_test.go
   - `accounting: admin API + RBAC (step 6)` — proto/admin/admin/admin.proto, dto/accounting.go,
     apisrv/admin/accounting.go, rbac.go
   - `accounting: reports + reconciliation (step 7)` — store/accounting/{reports,reconcile}.go,
     accounting_reports_integration_test.go
   - `accounting: dashboard alerts (step 8)` — metrics/{dashboard,settings}.go, entity/metrics.go
   - `docs: accounting implementation plan` — docs/plan-accounting/

## Фаза 1 — сборка и тесты бекенда (гейт: всё зелёное до пуша)

1. `gofmt -l ./internal/ ./app/ ./config/` → если что-то показал — `gofmt -w` этих файлов,
   доклей в соответствующий коммит (amend/fixup).
2. `make build` — первый настоящий компайл: buf-кодоген pb-типов новых RPC, mockery
   (mock_Accounting.go), swagger, компиляция. Падения чинить минимально (вероятные: импорты,
   pb-имена в dto/apisrv — сверяй с сгенерённым proto/gen/).
3. Юнит и статика: `go test ./internal/accounting/... ./internal/rbac/...
   ./internal/store/migrationlint/... -count=1`.
4. Интеграционные (нужна MySQL из config/config.toml; БД дважды мигрирует 0189/0190 —
   идемпотентность): `go test ./internal/store/ -run 'TestAccounting|TestAcct' -v -count=1`.
   После — `SELECT COUNT(*) FROM acct_account;` == 34.
5. `make lint`.
6. Всё зелёное → допушить fixup'ы в коммиты (rebase -i autosquash), НЕ пушить пока.

## Фаза 2 — контракт (зеркало)

1. В grbpwr-proto: убедись, что коммит f64a6b72cd9497e9b3768d32a82aac060c66645b существует
   и запушен в default-ветку GitHub (клиентский сабмодуль на него указывает). Если он только
   локальный — push.
2. Проверь байт-идентичность: `cmp grbpwr-products-manager/proto/admin/admin/admin.proto
   grbpwr-proto/admin/admin/admin.proto` (и что f64a6b72 не содержит НИЧЕГО кроме
   accounting-изменений; если там замешан чужой PLM-контракт — стоп, спроси меня).
3. В бекенде: `echo f64a6b72cd9497e9b3768d32a82aac060c66645b > proto/contracts/mirror-git-ref.txt`,
   затем `PROTO_MIRROR_DIR=../grbpwr-proto make check-proto-contracts` — должен пройти без
   правки baseline'ов (изменения аддитивны). Закоммить mirror-git-ref.txt
   (`proto: pin mirror at accounting contract`).

## Фаза 3 — бекенд на бету

1. `git push origin feature/accounting-core` → PR в `beta` (или локально:
   `git checkout beta && git pull && git merge feature/accounting-core` — конфликтов быть не
   должно; есть — стоп, спроси) → push beta. DO auto-deploy применит миграции
   (MYSQL_AUTOMIGRATE=true на бете).
2. Env беты (через DO UI/doctl или локальный .do/app-beta.yaml + apply — как у нас принято):
   `ACCOUNTING_ENABLED=true`, `ACCOUNTING_START_DATE=<сегодня, YYYY-MM-DD>`. Остальные
   accounting-энвы — дефолты.
3. Верификация деплоя: `curl https://backend-beta.grbpwr.com/readyz`; в /statusz (нужен
   admin-токен) — воркер acctposting жив; Swagger UI (корень) показывает accounting-RPC;
   `GET /api/admin/accounting/accounts` с админ-токеном → 34 счёта.

## Фаза 4 — клиент на бету

1. `cd grbpwr-admin-client && git status`. ВАЖНО: ветка feat/plm-cutover — проверь
   `git log beta..HEAD --oneline` и stash/дифф: если на ветке чужой PLM-WIP помимо
   accounting — спроси меня, как разъезжаться (вероятно: новая ветка feature/accounting-ui
   от beta, cherry-pick/перенос только наших файлов: src/components/managers/accounting/**,
   backend-alerts.tsx, page/components/index.ts, page/index.tsx, routes.ts, src/index.tsx,
   сабмодуль proto, сгенерённый admin/index.ts, docs/plan-accounting-ui/).
2. Коммиты: `contract: bump grbpwr-proto to accounting + regenerate clients` (сабмодуль +
   proto-http) и `accounting: admin UI (journal, accounts, reports, periods, alerts bridge)`
   (+ `docs: accounting UI plan`).
3. `yarn build:check` (tsc+vite) и `yarn fix` (eslint+prettier — локально eslint работает;
   правки от --fix доклей в коммит). Оба зелёные — гейт.
4. `yarn dev` c `VITE_SERVER_URL=https://backend-beta.grbpwr.com` — ручной прогон перед
   мерджем (см. Фазу 5, пункты UI).
5. Merge в `beta` клиента → push → Vercel задеплоит admin.beta.grbpwr.com.

## Фаза 5 — сквозной смоук на бете (чек-лист из планов 08/10)

Бекенд-события: тестовый заказ картой (Stripe TEST) → в течение ~минуты проводка S1 в
журнале; рефанд этого заказа → S2 (uuid:1); приход материала с ценой → M1; receive
производственного рана → P1; opex-строка текущего месяца → O1 (тик воркера).
UI: логин на admin.beta → пункт accounting виден (супер-аккаунту); Accounts — 34 счёта,
создать/переименовать тестовый; Journal — фильтры/деталка/сторно тестовой ручной проводки;
ручная проводка с пресетом Stripe payout + кнопка balance; Reports — TB balanced, P&L с
caveats, BS CHK=0, drill TB→ledger→entry→заказ, copy table вставляется в таблицу; Recon —
блоки и дельты осмысленны; Periods — модалка close прошлого месяца показывает светофор
(закрывать месяц НЕ обязательно); главный дашборд — backend-алерты рендерятся (если есть).
RBAC: аккаунтом без accounting-прав пункт меню скрыт.

## Фаза 6 — отчёт мне

Список коммитов/PR по обоим репо; вывод тестов (короткие итоги); URL'ы беты; результаты
каждого пункта смоука (ok/fail); что отложено (известные follow-up'ы: acct_posting_lag_hours
в pb AlertSettings; CSV-экспорт; интеграционный тест алертов). Прод НЕ трогаем — merge в
master только после недели soak, отдельным решением.

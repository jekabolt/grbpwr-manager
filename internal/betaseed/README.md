# betaseed — beta environment seeder

Populates the **beta** environment (`backend-beta.grbpwr.com` — the only host it will
talk to) with representative data across **every admin function**, so you can log into
beta and see the whole product working without creating entities by hand. Re-runnable
and idempotent: every run mints fresh, uniquely-suffixed entities; dictionaries, hero and
archive are reused.

## Run

```bash
# full seed (default volume = dense)
go run ./cmd seed

# smaller/larger data sets
go run ./cmd seed --volume=single     # 1 style
go run ./cmd seed --volume=moderate   # ~5 styles
go run ./cmd seed --volume=dense      # ~15 styles + ~120 orders (default)

# run only some phases (analytics implies catalog+plm, which it orders against)
go run ./cmd seed --only=catalog,plm
go run ./cmd seed --only=extras

# read-only coverage table — what does beta hold right now?
go run ./cmd seed --only=verify

# trace every RPC
go run ./cmd seed --verbose
```

Credentials: logs in as `beta-seed-bot`; on first run the account is bootstrapped from
`AUTH_MASTER_PASSWORD` in `.do/app-beta.yaml` (never printed). Override with `--user` /
`--password` / `$BETA_SEED_PASSWORD` / `--master-yaml`.

## Phases

| Phase | File | What it seeds |
|---|---|---|
| catalog | `catalog.go` | dictionaries, media, styles → colourways → variants → size charts → **publish** → stock, hero, archive, a storefront order + a custom order. N published, stocked, storefront-visible styles. |
| plm | `plm.go` | one tech card carried through the full PLM flow A–L: sketches/pieces, materials (4 typed classes) + BOM, 2 colourways + recipe, 2 sample/fitting rounds, spec release, costing (estimate→actual), label/packaging assembly, a production run (sets `cost_price`), publish, reserve→ship→return→release, negative/optimistic-lock checks. |
| extras | `extras.go` | the otherwise-empty admin sections: promo codes, showroom models, tasks/kanban, admin accounts, colours/tags, platform config (carriers, payment fees, hero colour, settings), members (via storefront newsletter) + tier/status/hacker-invites, support tickets, order reviews. |
| analytics | `analytics.go` | VAT/FX/opex/employees/channel-spend/inventory-targets/alert-settings, then ~120 net-revenue orders (custom orders born Confirmed, progressed through shipped/delivered/partial-refund across 12 countries, some on the PLM cost-priced style), plus fulfillment-board processing. Then asserts revenue > 0 and every metrics section is populated. |
| accounting | `accounting.go` | the double-entry ledger: a Volume-scaled spread of **balanced manual journal entries** across revenue + opex + asset/liability (so Trial Balance, P&L and Balance Sheet all show rows), one **reversal** (reversal-not-edit), and a **period-lifecycle** touch. Self-contained (no catalog/plm deps → runs standalone via `--only=accounting`). Then asserts the ledger reads back **balanced** (Σdebit == Σcredit), non-empty and reconciliation-queryable. Chart of accounts is not seeded — the 34 system accounts come from migration `0190`. |

Every run ends with a `PrintCoverage` read-back table.

## Architecture

- Typed **protojson over the grpc-gateway** — native gRPC is not reachable through the
  DigitalOcean HTTP ingress. `rpc_gen.go` is generated (`go run ./tmp/seedgen`) from the
  `google.api.http` annotations (regenerate from repo root: `go run ./internal/betaseed/gen`),
  giving one typed wrapper per RPC (`c.CreateTechCard(ctx, req)`).
- Prices are built from `internal/currency.RequiredCurrencies()` (7 incl **PLN**) — no
  hardcoded currency list, so a required-currency change can't silently break publish.
- Host-guarded: `NewClient` refuses any host but `backend-beta.grbpwr.com`.

## Honest limitations (documented, not faked)

- **Delivery lead-time durations, FORECAST, COHORT_RETENTION, compare-vs-previous-period**
  stay thin: every REST order is stamped `placed = now` and its status history lands within
  seconds, so day-scale durations round to 0 and there is no prior-month history. Filling
  these would require backdating rows directly in beta MySQL (out of scope: REST-only).
- **GA4/BigQuery panels** (funnel, web-vitals, device/session, campaign attribution, notify-me,
  OOS-impact) stay empty — they are fed by the `ga4sync` worker from real storefront traffic,
  which beta does not have. Marketing spend/ROAS still populate from operator-entered channel spend.
- **Accounting posts only MANUAL journal entries.** The admin `CreateJournalEntry` RPC forces
  `source_type=manual`, so `order_sale` / `order_refund` / `opex_month` / `material_*` / `production_receive`
  entries cannot be fabricated over REST — those are the `acctposting` worker's job, derived from the
  operational facts the other phases already seed (orders, opex, production runs). Given
  `ACCOUNTING_ENABLED=true` on beta, the worker posts them asynchronously (on its ticker, ~1 worker
  interval), so they appear alongside the manual entries a short while after a full seed — not
  deterministically within one run. **Period close** also soft-skips on beta: `ACCOUNTING_START_DATE`
  means there is no fully-past in-window month to reconcile-and-close yet, so `CloseAcctPeriod` returns
  `closed=false` (not an error) and the phase leaves the current period open.

## Beta data issues surfaced while seeding (not seeder bugs)

- **Country dictionary is empty on beta** (`GetDictionary().Countries == 0`); `SetCountryActive`
  returns `country "US" not found`. Migration `0154_merch_dictionaries.sql` seeds ~250 countries,
  so beta likely never applied it (or the table was reset). There is no create-country RPC, so this
  cannot be seeded — it needs the migration re-applied. Affects real country management too.
- **Sendcloud has no shipping rules on beta**, so `GenerateShippingLabel` (400 "no shipping rules
  define ship_with") and `SchedulePickup` (500) fail; `PrepareShippingLabel`/`GetShippingOptions`
  work. The analytics phase treats these best-effort (WARN, non-fatal).

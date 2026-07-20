# Wave 2 — synthesized implementation spec (deep-reasoner ⊕ Codex)

Two independent design passes were run (reasoner + Codex) and reconciled. This is the authoritative
build spec; where they diverged, the decision + rationale is recorded. Base: worktree
`grbpwr-wt-acct-phase2` off `origin/beta`. Migration number **0195**.

## Core ledger model (per order, keyed by order UUID)

| Moment | source_type / source_key | Lines |
|---|---|---|
| Payment (S1n) | `order_prepayment` / `uuid` | Dr mAcc **G** · Cr 2070 **VAT** · Cr 2090 **G−VAT** · fee Dr 6050/Cr 1030. **No COGS.** |
| Shipped | `order_transit` / `uuid` | Dr 1140 / Cr 1130 at Σ `cost_price_at_sale` |
| Delivered (S1d) | `order_delivered_sale` / `uuid` | Dr 2090 **(posted remaining 2090)** · Cr 4020/4010/4310 **NET** · Cr 4110 **SHIP** · Dr 5010 **COGS** / Cr 1140 **(posted 1140)** |
| Refund pre-delivered | `order_refund` / `uuid:seq` | Dr 2090 **(net)** · Dr 2070 **(vat)** · Cr mAcc **r** · if transit posted: Dr 1130 / Cr 1140 returned-cost |
| Refund post-delivered | `order_refund` / `uuid:seq` | **existing `BuildOrderRefundEntry` (S2) unchanged** |

Invariant: after delivery the new chain's cumulative movement equals old S1 (2090 and 1140 both net to
zero). VAT is credited **once**, at payment (Cr 2070 in S1n); delivery never touches VAT.

## Decisions on the divergences (this is the synthesis)

- **D1 — S1d drains the EXACT posted balances (adopt Codex).** `BuildOrderDeliveredSaleEntry` takes
  `prepaymentNet` and `transitCost` as inputs (the worker reads them from the posted chain), rather than
  recomputing G−VAT. This makes 2090/1140 drain to exactly zero regardless of (a) a `vat_rate` edit
  between payment and delivery (kills reasoner-R5) and (b) a partial pre-delivery refund that already
  reduced 2090 (kills reasoner-R6). Revenue = `splitRevenue(gross, prepaymentNet)` balancing difference;
  shipping = `shipmentCost·k` rounded.
- **D2 — synthetic transit at delivery if missing (adopt Codex).** Because S1d drains 1140, a transit
  entry must exist first. If an order went Confirmed→Delivered with no shipped event (legal at
  `lifecycle.go:393-400`), the worker posts a synthetic `order_transit` (Dr 1140/Cr 1130 at Σcost,
  occurred_at = delivered) **before** S1d, both in one short Tx. (Supersedes the reasoner's alternative
  of crediting 1130 directly — exact-drain needs the 1140 leg.)
- **D3 — VAT filing fix is IN SCOPE and already applied.** `vatreturn.go` hard-coded
  `source_type IN ('order_sale','order_refund')` — post-cutover VAT posts on `order_prepayment`, so JPK/OSS
  would under-report. Fixed at all 4 sites: 2070 VAT + net base now read `('order_sale','order_prepayment',
  'order_refund')`, net base additionally sourced from **2090** (the tax-point base for the new chain),
  `order_delivered_sale` deliberately EXCLUDED (its revenue is a later period). PL advance-payment
  (zaliczka) treatment — flag for accountant sign-off on rollout. (Reasoner trusted the plan's "agnostic"
  claim; Codex read the SQL. Codex right.)
- **D4 — recon block needs explicit wiring (adopt Codex).** A new `Prepayments` recon block does NOT
  "appear automatically": add `AcctReconBlock prepayments = 9` to `GetAcctReconciliationResponse` (proto,
  additive → mirror cycle), map in `dto.ConvertAcctReconciliationToPb`, add `prepayments` to the client
  `reconciliation.tsx` BLOCKS. BS/PL reports DO auto-include 2090/1140 (seeded rows, `sectionBalance`
  groups by section) — no reports.go change, so no collision with the reports track.
- **D5 — no-op Down migration (adopt Codex).** Narrowing the CHECK after wave-2 rows exist would fail;
  seeded accounts may be referenced. Down = `SELECT 1` (append-only philosophy; matches 0190). *(the
  written 0195 currently reverts the CHECK — switch it to no-op before finalize.)*
- **D6 — config validation (adopt Codex).** `delivered_recognition_from` must reject a date **before**
  `accounting.start_date` (a cutover before the ledger exists is nonsense). Empty = feature off. A future
  date is allowed (pre-arms the switch).
- **D7 — facts carry PaidAt/ShippedAt/DeliveredAt (adopt Codex).** `GetOrderFactsForPosting` joins the
  `order_paid` outbox row (payment date — NOT `payment.modified_at`, an ON UPDATE ts), and shipment
  shipping/delivered dates. Needed for the policy decision at paid and the refund defer-guard.
- **D8 — refund defer-guard (adopt Codex).** If `DeliveredAt.Valid && !order_delivered_sale exists`, the
  refund must **defer** ("awaiting delivered sale posting"), not take the pre-delivered branch — a failed
  delivered event must not misroute a refund into unwinding 2090 that is about to be drained.
- **Boundary (both agree): `paidAt.Before(cutover)` = old; `== cutover` and later = new** (mirrors the
  phase-1 startDate `<` rule). Custom/cash born-Confirmed orders keep old S1 forever (07 §7.4.4).

## New store point-read (Codex `GetOrderPostingState`)

One aggregate over entries whose order key is `SUBSTRING_INDEX(source_key, CHAR(58), 1)`, returning:
`LegacySale, Prepayment, Transit, DeliveredSale bool` + signed balances `Remaining2090, Remaining1140,
RemainingVAT, OriginalVAT decimal`. Replaces N× `EntryExistsBySource` and gives S1d/refund the exact
drain amounts. Add to `dependency.Accounting`; regenerate mock (mockery + BSD/GNU-sed fix — see project
memory `makefile-gnu-sed-mocks-gotcha`; mocks are gitignored/absent in a fresh worktree).

## Worker branch table (`outbox.go`)

- `order_paid`: empty cutover / pre-cutover / cash / bank-invoice → `BuildOrderSaleEntry` (old S1).
  Post-cutover Stripe → `BuildOrderPrepaymentEntry` + `SetOrderVatRegime` (same Tx). Opposite-policy chain
  already present → needs-review (never post the second chain).
- `order_shipped`: old/custom → skip "pre-policy order". New without prepayment → defer "awaiting
  prepayment posting". Else post `order_transit`. `ErrSkipEmpty` (uncosted) = processed.
- `order_delivered`: old/custom → skip. Require prepayment. If no transit → synthesize it (D2). Pass exact
  `Remaining2090`/`Remaining1140` to S1d. `postOrDefer` generalized to accept ≥1 entry in one Tx.
- `order_refund`: `LegacySale && Prepayment` → needs-review (mixed). `LegacySale` → S2. `Prepayment &&
  DeliveredSale` → S2. `Prepayment && !DeliveredSale` → pre-delivered refund. `DeliveredAt.Valid &&
  !DeliveredSale` → defer (D8). No sale/prepayment → existing bounded orphan defer/review.

## Event capture (`lifecycle.go`) — both agree

- `SetTrackingNumber` (~:142, closure ~:147): enqueue `order_shipped`, `OccurredAt =
  shipment.ShippingDate` (first-ship instant; re-ship = duplicate no-op).
- `DeliverOrderWithSource` (~:422, closure ~:424): hoist `deliveredAt` so transition + event share it;
  enqueue `order_delivered` only on real transition. Single choke point for all 4 delivery paths.

## File map (⊕ = done this session)

⊕ `internal/store/sql/0195_accounting_delivered.sql` (switch Down to no-op — D5)
⊕ `internal/entity/accounting.go` (consts, Valid maps, payloads, `Prepayments` recon field) — MAY need
   `PaidAt/ShippedAt/DeliveredAt` on `AcctOrderFacts` + `FinalRefund` on refund payload (D7)
⊕ `internal/accounting/accounts.go` (2090/1140) · ⊕ `accounts_test.go` (39)
⊕ `internal/store/accounting/vatreturn.go` (D3 — VAT filing fix)
⊕ `internal/store/migrationlint/enum_drift_test.go` (re-point to 0195)
— `internal/accounting/orderdelivery.go` (new: S1n, transit, S1d, pre-delivered refund) + `common.go`
   shared `paymentGrossVAT`/`splitRevenue` helpers
— `internal/acctposting/{acctposting.go,worker.go,outbox.go}` (config + branch table)
— `internal/store/accounting/{ledger.go,orderfacts.go,reconcile.go}` (GetOrderPostingState; facts; recon
   rebasing + reconPrepayments) + `dependency/dependency.go` + mock
— `internal/store/order/lifecycle.go` (2 enqueue points)
— `config/cfg.go` (+ bindEnv) · `.do/app.yaml` (empty) · `.do/app-beta.yaml` (cutover date)
— proto `GetAcctReconciliationResponse` +prepayments · `dto/accounting.go` map · mirror + client submodule
— admin client `accounting/utils/constants.ts` (3 labels) · `reports/components/reconciliation.tsx` (block)
— unit tests: builders, worker branches, config

Guardrails: EUR, decimal, append-only (reversal not edit), round2 only on final balancing lines, RBAC
`accounting` on new RPCs (none new here — recon reuses GetAcctReconciliation), NEVER `go test
./internal/store/...` (prod DB). Verify: `go build ./...` + `internal/accounting` unit tests + beta smoke.

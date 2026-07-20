# Phase-2 execution scope & plan review (this worktree)

Worktree `grbpwr-wt-acct-phase2`, branch `feat/accounting-w2-delivered` off `origin/beta`
(@ f180e3c). Review date 2026-07-20. Reviewer: orchestrator (Fable/Opus).

## Plan review verdict

The existing `docs/plan-accounting-phase2/` (master 00 + waves 01–06 + notes 07) is **sound and
still valid** after waves 0–1 shipped. Anchors spot-checked against current code hold
(`store/order/lifecycle.go` DeliverOrderWithSource / SetTrackingNumber single-capture points;
`acct_journal_entry.source_type` CHECK + `acct_event.event_type` CHECK in `0189`; builders in
`internal/accounting/`; worker `internal/acctposting/`). No rewrite needed — we execute it with
two scope edits below.

## Scope edits vs the written plan

1. **Crypto is OUT** (owner decision, 2026-07-20). Wave 4.2 (USDT wallet, accounts 1050 / 7010 /
   7020 / 7030, FX gain-loss templates) is **dropped from this effort**. BTC/ETH were already out.
   The three other Wave-4 blocks (Revolut CSV, Stripe disputes, AP/AR) stay in.
2. **Wave 5 reporting is a PARALLEL track** (owner is running reports separately). This worktree
   does **not** build: Cash Flow (`GetCashFlowStatement`), Financial Health ratios
   (`GetFinancialHealth`), or the GBP / FRS-105 filing pack. Year-end close (5.3) is a
   posting/process, not a report — left to the reports track as well unless they ask us to take it.

## What THIS worktree implements (in order)

| # | Wave | Content | Task |
|---|------|---------|------|
| 1 | **W2** | delivered-recognition: 2090 Customer Prepayments, 1140 Inventory in Transit, order_shipped/order_delivered events, cutover policy, refund branches, recon | #2 |
| 2 | **W3** | full P&L: 6030 actual shipping, dev-expenses→6210, 4030 discounts line, 2050/8010 Corporation Tax | #3 |
| 3 | **W4−crypto** | Revolut CSV inbox (Unsorted sheet), Stripe disputes, AP/AR subledgers | #4 |

Order rationale (from master 00): W2 first — it changes BS structure (2090/1140) that the parallel
reports track needs; it is the critical-path prerequisite for their Cash Flow / filing work. W3
parallelizes with W2 in the plan but we do it second (shares the revenue-block builder with W2's
S1d — one merge, no divergence). W4 after W2 (delivered semantics settled).

## Migration numbering

Next-free on `origin/beta` = **0195**. Re-verify `next-free` at the start of each wave (many
parallel branches — plm ws1–8, finish-rework tracks, economics, analytics, the reports track — may
claim numbers on merge; `migrationlint` catches collisions). Named CHECK constraints only, idempotent
`information_schema`-guarded `PREPARE` pattern (07 §7.2).

## Collision boundary with the parallel reports track

Shared file of concern: `internal/store/accounting/reports.go`.
- **We touch** its P&L/BS section builders only to add rows/lines inseparable from posting changes:
  W2 adds 2090/1140 to the BS sections; W3 adds the 4030 discount line, the Net-Profit-after-tax
  total, and flips the two permanent caveats to conditional.
- **They own** brand-new report surfaces: `GetCashFlowStatement`, `GetFinancialHealth`, GBP/FRS-105
  filing, and (tentatively) `CloseFinancialYear`.
- Coordination rule: if both need `reports.go` in the same window, we land W2/W3 first (they depend
  on our BS/P&L structure anyway) and rebase the reports track on top. Keep our reports.go edits
  additive and localized.

## Guardrails (from phase-1 rules, still in force)

- Ledger append-only; reversal-not-edit. Money = `shopspring/decimal`. Amounts EUR (GBP is a
  wave-5 on-the-fly translation, not a second ledger currency).
- NEVER run `go test ./internal/store/...` locally — it targets the prod DB and DROPs tables.
  Verify via `make build` + unit tests + beta deploy smoke.
- RBAC section `accounting` on every new RPC. Proto contract via the grbpwr-proto mirror
  (byte-for-byte + mirror-git-ref bump). New DI deps behind `internal/dependency` + generated mock.
- `uncosted` never invented — unposted movements surface in reconciliation, not guessed.

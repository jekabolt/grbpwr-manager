# 13 — Statutory review: are the books and filings safe to stand behind?

Session 2026-07-24, branch `fix/accounting-audit`. Companion to `12-audit-findings.md` (code-level
audit). This one answers a different question: **would an accountant / tax office accept what this
module produces?** Two deep passes: (A) every statutory report generator vs what the filing legally
requires; (B) a completeness matrix — does every money-moving action in the system create a correct
journal entry ("no orphan money").

## 0. Executive verdict

The **double-entry core is genuinely solid**: regime classification is mutually exclusive (one sale
lands in exactly one VAT return), the VAT tax point is consistently the payment instant (correct
for prepaid B2C under PL art. 19a(8)), refunds net proportionally, the automated posting paths
(S1/S1n/delivered chain, refunds, disputes, M1–M8 materials, P1 production, opex, shipping,
dev-expense, depreciation, corp-tax) are balanced, idempotent and drift-safe against edits.

But **none of the generated statutory files is submittable as-is**, and there is one systemic
money leak. Three blockers dominate everything else:

1. **Currency.** The ledger is EUR. JPK_V7M must be filed in PLN (art. 31a — NBP D-1 rate) and the
   UK return in GBP; both exporters emit raw EUR figures with no conversion layer (`internal/jpk/
   declaration.go:58` "rounds a base-currency amount to whole złoty" — of EUR; `ukvat.go:32`). The
   numbers are wrong by the FX factor (~4.3× for PLN). There is no NBP rate source in the codebase
   at all. OSS is the only VAT filing in the right currency (EUR).
2. **Input VAT is captured only on material receipts.** `opex_line` and production-run costs carry
   no VAT field, so VAT on rent, software, marketing, professional services, CMT/logistics — every
   non-material invoice — books gross into expense and is **never reclaimed**. Net VAT payable is
   systematically overstated, expenses overstated. This is the single most expensive gap.
3. **Payroll statutory layer does not exist.** Salaries book as one lump to 6330; there are no
   accounts or postings for employer social contributions (ZUS/NI) or withheld PIT, and no PIT-4R /
   ZUS DRA outputs. If people are on payroll, this is handled entirely outside the system today.

Treat every generated file as an **accountant's input draft**. The books (ledger) are trustworthy
for management; the filings need the P0 work below before "no problems with the tax office" is a
claim anyone can make. A licensed accountant must stay in the loop for the filings regardless —
this module can get to "accountant verifies and submits", not to "nobody looks".

## 1. Filing submittability

| Filing | As-generated? | Blocking issues |
|---|---|---|
| JPK_V7M declaration (P_*) | **No** | EUR-as-PLN; no input-VAT boxes (P_40/41/43/44 absent), P_51 = output-only and disagrees with the app's own NetPayable (`vatreturn.go:206` vs `jpk/declaration.go:105`) |
| JPK_V7M sales evidence | **No** | same currency; buyer name "-" placeholder, rows stamped with placed date but selected by payment date (`jpk/evidence.go:91-94`), refunds netted instead of korekta rows |
| OSS / VIU-DO | **With post-processing** | currency correct (EUR ✔), per-country derived rates ✔; refunds must move to a corrections section referencing the original quarter (negative country lines can't be filed); country×rate split if a reduced rate ever applies |
| UK VAT 9-box | **No** | EUR-as-GBP; `uk_stock_domestic` assumes UK stock with no inbound transfer / postponed VAT accounting; Box 7 misses UK opex; UK VAT registration itself unconfirmed |
| VAT-UE (recapitulative, WDT/WNT) | **No — not generated** | legally required alongside JPK when WDT/WNT occur |
| Intrastat | **No — not generated** | required once thresholds crossed |
| FRS-105 accounts | **With post-processing** | entity identity unproven (UK Ltd vs sp. z o.o. — the whole tax surface is Polish; if PL entity, FRS-105 is the wrong framework entirely); no share-capital account (equity is Owner's/Draws); CreditorsAfterYear never populated |
| CT600 / CIT-8 | **No** | the 8010 accrual is a book provision at an operator-typed rate on unadjusted accounting profit (no depreciation add-back / capital allowances, no disallowables) — fine as a provision, not a computation |
| PIT-4R / ZUS DRA | **No — not modelled** | see blocker 3 |

What IS verified correct in the VAT layer: regime partitioning (`pl_domestic|wdt|export` → JPK,
`oss` → OSS, `uk_stock_domestic` → UK; no sale double-counts across filings), tax point at payment
across the delivered chain (2070 credited once on `order_prepayment`; `order_delivered_sale`
excluded from every VAT query), WNT/import self-charge with both legs in the same period, NIP
checksum validation in the JPK taxpayer block.

## 2. Posting completeness — where money can go missing

Full matrix in the audit transcript; the rows that matter, ranked:

**P0 — money misstated**
1. **Input VAT on services** (see blocker 2). Fix spec: add `vat_amount`/`vat_regime` to
   `opex_line` and production-run costs (migration + UI), post `Dr 2080` on the accrual, include in
   `vatreturn.go` input aggregation.
2. **1030 (Stripe) has no reconciliation.** Payouts 1030→1010 are manual bank-inbox posts and no
   recon block asserts the 1030 balance (recon's bank block covers 1010 only) — a missed payout
   post misstates cash silently. Fix spec: recon block comparing 1030 ledger vs the Stripe balance
   API (client already exists in the codebase), or at minimum surface the 1030 balance + last
   payout date on the recon screen.
3. **Payroll taxes** (see blocker 3). Fix spec: accounts for employer contributions expense +
   liability and PIT/ZUS withholding liabilities, an opex category or dedicated posting, and the
   payment legs; filings stay with the accountant.
4. **Accrual balances unreconciled.** 2030/2010 have no balance-vs-source recon, so paying a bill
   to 6xxx instead of clearing 2030 double-counts expense undetected (FE warning exists; server
   check doesn't). Fix spec: recon blocks for 2030 (accrued vs unpaid opex/shipping) and 2010
   (AP vs unpaid receipts).
5. **USDT/crypto orders are unbookable.** USDT is a valid order currency but no crypto asset
   account was ever seeded and non-Stripe non-EUR orders dead-letter to review with nowhere to
   post. Fix spec: either seed 1050 + FX accounts and a template, or formally drop USDT.

**P1 — latent / classification**
6. No realized/unrealized **FX gain-loss account** — folding at today's rate and settling at
   another leaves the difference nowhere to go; Revolut EXCHANGE legs ignored on both sides drop
   the real spread (surfaced only as a recon note).
7. **Equipment acquisition** is manual and unreconciled against the fixed-asset register
   (`fixedasset.go:16` — register deliberately doesn't post the purchase): an asset can depreciate
   without its 1220 debit ever existing. Add a 1220-vs-register recon or post acquisitions from
   the register.
8. Shipping-only refunds debit 4040 (returns) rather than contra-4110 — P&L split cosmetics.
9. Refund proportion `k` recomputes from the live order total (`refund.go:40`) — wrong only if a
   total is edited post-payment; totals are immutable by convention today.
10. 2060 (loans) referenced by reports but never seeded (harmless no-op); owner equity/draws and
    loan legs are manual with no recon.

**Verified non-issues:** movement cost "backfill" cannot happen (no code path updates
`unit_cost_movement.unit_cost_base` after insert — corrections are M8 movements, which post
normally); uncosted movements are surfaced by reconciliation; movement/opex/shipping edits
re-post via versioned keys; order events are idempotent; Σdr=Σcr enforced at the single write path.

## 3. Recommended order of work

1. **Now (before any filing is trusted):** hand the accountant this document + the app's VAT
   summary screens as *drafts*; they file from their own systems. Confirm the two identity
   questions that gate everything: the legal entity/jurisdiction (PL sp. z o.o. vs UK Ltd) and
   whether a UK VAT registration exists.
2. **P0 engineering:** NBP D-1 rate layer + PLN-denominated JPK (mirror `fxsync` for NBP); input
   VAT on opex/services; JPK declaration input boxes + P_51 aligned to NetPayable; GBP conversion
   for the UK return (via `costing_fx_rate` as the overview always intended); OSS corrections
   section; 1030 recon; payroll accounts.
3. **P1:** VAT-UE export, Intrastat when thresholds near, FX gain/loss accounts, fixed-asset
   acquisition flow, evidence-row invoice metadata (real buyer names, sequential invoice numbers,
   korekta rows) — the last requires storing invoice identities, which the order model doesn't
   have yet.

## 4. Honest limits of this review

Static code review against filing rules as known to the reviewers; no run against a live DB, no
protoc/go build in the review environment, and no sight of the actual legal-entity documents. The
two agents' findings were cross-checked against the code cited above, but rates, thresholds and
schema versions (JPK_V7M(2), VIU-DO, CT/CIT) change — the accountant owns final correctness of any
submission. "100% no problems" is reachable only after the P0 list plus a professional's sign-off
on one real filing cycle.

## 5. Update (same day): the P0 filing layer is IMPLEMENTED

Entity confirmed by the owner: **UK Ltd with a Polish VAT registration (NIP), no Polish company** —
FRS 105 / UK CT is the right framework; the Polish side is the VAT registration of a foreign
entity. Landed on this branch (regen `make proto` + mockery, then `go test ./internal/...`):

- **Currency layer**: per-transaction D-1 daily conversion via `costing_fx_rate` (the fxsync ECB
  feed already stores dated daily rows incl. PLN/GBP). Poland: ECB rates are used under the
  art. 31a ust. 2a election (12-month binding — CONFIRM the election with the accountant; an NBP
  source can replace the feed later without touching the report code). Missing rates FAIL the
  export loudly, listing the dates.
- **JPK_V7M**: `GetVatReturnPLFiling` (PLN), evidence rows stamped/converted at the tax point with
  real buyer names, B2B refunds as separate `-KOREKTA` rows, a REAL purchase register
  (`ZakupWiersz`: domestic material receipts + documented opex invoices), declaration
  P_42/P_43/P_48 filled and P_51/P_53 = NetPayable math. Undocumented opex VAT stays out of the
  XML (caveated) so declaration ↔ register always cross-check.
- **Input VAT on services** (P0-1): `opex_line` carries vat_amount/regime + invoice identity
  (0203); the monthly accrual books Dr 6xxx net + Dr 2080 VAT / Cr 2030 gross.
- **UK return**: `GetUkVatReturnFiling` in GBP; Box 4/7 include domestic_uk opex input.
- **OSS**: refunds of earlier quarters are now correction lines keyed to the original quarter
  (rows/XML `Korekty`), never netted.
- **VAT-UE**: new `GetVatUe` RPC (WDT by buyer VAT id / WNT by supplier VAT id, PLN).
- **Accounts** (0204): 3005 Called-up Share Capital (own FRS105 line), 2045 Payroll Taxes Payable,
  6335 Employer Social Contributions (opex category `employer_social`), 2060 Loans; FRS105 maps
  2015/2060 to creditors-after-year. **1030 Stripe recon block** added (informational balance).

Still with the accountant (unchanged): CT600 computation (8010 stays a book provision), PIT-4R/ZUS
runs (accounts now exist; filings external), Intrastat, invoice numbering (order refs stand in for
invoice numbers until an invoicing module exists), and the art. 31a ECB election + UK VAT
registration confirmations. FE screens for the new fields (opex VAT inputs, VAT-UE view, stripe
recon row) follow after the client regen.

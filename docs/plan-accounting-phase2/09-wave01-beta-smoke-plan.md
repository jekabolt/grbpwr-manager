# Wave 0–1 (VAT) — beta smoke plan

Run after the `feat/betaseed-accounting` branch is deployed to **beta** (`backend-beta.grbpwr.com`,
DB `grbpwr_beta`, `MYSQL_AUTOMIGRATE=true`). Migration **0193** (GB `vat_rate` 20%) auto-applies on that
deploy — verify it landed before scenario 4:

```sql
SELECT country_code, rate_pct FROM vat_rate WHERE country_code IN ('GB','DE','PL');
-- expect GB 20.00, DE 19.00, PL 23.00
```

## Environment facts confirmed during finalization

- **Origin country is empty on beta AND prod** (`SHIP_FROM_COUNTRY=""` in both specs). This is **harmless**
  for VAT: `ResolveVatRegime` only reads origin for the `origin == 'GB'` (ship-from-UK) special case, which
  neither env triggers; every regime below is decided by **destination + payment method + buyer_vat_id**.
  No env change is required. (If grbpwr ever ships *from* the UK, set `SHIP_FROM_COUNTRY=GB` on that env so
  non-cash orders resolve to `uk_stock_domestic`.)
- The resolver checks **cash first**: a *cash* order is `uk_stock_domestic` even if it carries a
  `buyer_vat_id`. A WDT (B2B intra-community) sale therefore needs a **non-cash** custom order.

## Order scenarios

Post each, then inspect the `order_sale` journal entry (Swagger `GetJournalEntry` or the ledger) and the
`customer_order.vat_regime` snapshot. G = gross recognised (Stripe: `total_settled_base`; cash: total).

| # | Order to create | vat_regime | Rate | Expected `order_sale` lines |
|---|---|---|---|---|
| 1 | Storefront **card**, ship-to **DE** | `oss` | DE 19% | Dr 1030 G / Cr 4020 net / **Cr 2070 VAT** (+6050/1030 fee, +5010/1130 COGS) |
| 2 | Storefront **card**, ship-to **PL** | `pl_domestic` | PL 23% | Dr 1030 G / Cr 4020 net / **Cr 2070 VAT** |
| 3 | Storefront **card (Stripe)**, ship-to **GB** | `export` | — | Dr 1030 G / Cr 4020 G — **no 2070 line** |
| 4 | **Cash** custom order, no VAT id | `uk_stock_domestic` | GB 20% | Dr 1010 G / Cr **4010** net / **Cr 2070 VAT** (needs 0193) |
| 5 | **Non-cash** custom order (bank-invoice/card), `buyer_vat_id=DE123456789`, ship-to **DE** | `wdt` | 0% | Dr 1040 (bank) *or* 1030 (card) G / Cr **4310** G — **no VAT line**; reverse-charge invoice note = UI (deferred) |

Sanity: scenario 4 with a **€100** cash order posts exactly **1010 Dr 100 / 4010 Cr 83.33 / 2070 Cr 16.67**
(the store integration test `TestAcctPostingWorker/order_paid_posts_sale` pins this).

## Material receipt scenarios (need `costing:write` — your admin token has it)

| # | Receipt to create | input_vat_regime | Expected `material_receipt` lines |
|---|---|---|---|
| 6 | Receive material with `input_vat_amount`, regime **domestic_pl** | `domestic_pl` | Dr 1110 NET + **Dr 2080 input VAT** / Cr 2010 GROSS |
| 7 | Receive material, regime **wnt** | `wnt` | Dr 1110 / Cr 2010 (NET) + **Dr 2080 / Cr 2070** (self-charge, nets to zero) |

## Reports & reconciliation

8. `GET /api/admin/accounting/reports/vat-return?month=YYYY-MM-01` → output VAT by regime (domestic PL,
   WNT/import self-charge, OSS informational), input VAT by type, `net_payable`. Numbers must be sensible and
   internally consistent (WNT output == WNT input, net-zero).
9. `GET /api/admin/accounting/reports/oss-return?quarter_start=YYYY-MM-01` → per-country rows (DE from
   scenario 1) with applied rate, net, VAT; totals add up.
10. Reconciliation → the new **`vat`** recon block (2070 ledger vs regime-classified order sums) is **green**
    (delta 0). *(The named-parameter bug that made both reports error was fixed in finalization —
    `vatreturn.go` `SUBSTRING_INDEX(..., CHAR(58), 1)`.)*

## Wave acceptance criterion (01 §1.7)

**Run `vat-return` for the last already-filed month in parallel with the accountant's real manual JPK_VAT /
OSS submission and confirm the numbers match.** This requires the accountant's figures — it is the human
sign-off that closes the wave and cannot be automated here.

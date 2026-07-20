package accounting

import (
	"database/sql"
	"sort"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

const (
	// baseCurrency is the ledger's book currency. The whole accounting module is EUR-native
	// (docs/plan-accounting/00-overview.md). A pure builder cannot reach cache.GetBaseCurrency(),
	// so the base code is a package constant; if the base ever changes it changes here.
	baseCurrency = "EUR"

	// createdBySystem stamps automated postings (matches the acct_journal_entry.created_by DDL
	// default).
	createdBySystem = "system"

	// descMaxLen / caveatMaxLen bound the two free-text columns (VARCHAR(512), counted in
	// characters under utf8mb4) so a long material name or a pile of caveats cannot overflow them.
	descMaxLen   = 512
	caveatMaxLen = 512
)

// nullStr wraps a non-empty note as a valid sql.NullString (empty → invalid/NULL).
func nullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// isStripe reports whether a payment method settles through the Stripe processor — its money leg is
// 1030 and it carries a captured payment_fee.
func isStripe(m entity.PaymentMethodName) bool {
	return m == entity.CARD || m == entity.CARD_TEST
}

// moneyAccount is the account the gross order amount lands on, by payment method (04/S1):
// Stripe -> 1030 Payment Processor, cash -> 1010 Cash, bank-invoice -> 1040 Accounts Receivable (a
// custom order is Confirmed before its invoice is paid, so revenue is recognised against a
// receivable, not cash). Any unexpected method falls back to 1030, the overwhelming common case.
func moneyAccount(m entity.PaymentMethodName) string {
	switch m {
	case entity.CASH:
		return Acc1010
	case entity.BANK_INVOICE:
		return Acc1040
	default:
		return Acc1030
	}
}

// revenueAccount is the net-revenue account by payment method (04/S1): cash -> 4010 Retail / Popup,
// everything else -> 4020 DTC (Website).
func revenueAccount(m entity.PaymentMethodName) string {
	if m == entity.CASH {
		return Acc4010
	}
	return Acc4020
}

// VatDecision is the resolved VAT treatment BuildOrderSaleEntry posts by (phase 2, wave 1,
// docs/plan-accounting-phase2/01-wave1-vat.md §1.3). The worker fills it from ResolveVatRegime + the
// regime's vat_rate: Regime picks whether/how VAT is booked, RatePct is that regime's rate (0 for the
// no-VAT regimes export/wdt/none), and Caveats carries the resolver's advisory notes onto the entry.
type VatDecision struct {
	Regime  entity.VatRegime
	RatePct decimal.Decimal
	Caveats []string
}

// isB2B reports whether an order carries a buyer VAT id — a B2B / wholesale sale, whose revenue is
// credited to 4310 instead of the B2C 4010/4020 (§1.3). The resolver uses the same signal for wdt.
func isB2B(f entity.AcctOrderFacts) bool {
	return f.BuyerVatID.Valid && strings.TrimSpace(f.BuyerVatID.String) != ""
}

// saleRevenueAccount is the net-revenue account for a sale: B2B (buyer VAT id present) -> 4310
// Wholesale; otherwise the B2C account by payment method (4010 cash / 4020 DTC).
func saleRevenueAccount(f entity.AcctOrderFacts) string {
	if isB2B(f) {
		return Acc4310
	}
	return revenueAccount(f.PaymentMethodName)
}

// isBaseCurrency reports whether an order currency equals the book base (EUR), case-insensitively.
func isBaseCurrency(currency string) bool {
	return strings.EqualFold(strings.TrimSpace(currency), baseCurrency)
}

// oneCent is the absolute cent tolerance for the discount-reconstruction guard (07 §7.4.11).
var oneCent = decimal.NewFromFloat(0.01)

// revenueLines builds the net-revenue credit line(s) for a sale, splitting out a 4030 Discounts contra
// when a promo discount applies (phase 2, wave 3, feature 3.3). Without a valid, positive discount it
// returns the single credit of `net` to `revenueAccount` (unchanged phase-1/2 behaviour). With one it
// reconstructs the full-price net (fullNet = net / (1 − pct/100)) and returns a full-price CREDIT to
// `revenueAccount` plus a DEBIT of the reconstructed discount to 4030 — the two net to `net`, so the
// entry balance and the P&L total are unchanged; only the analytics split is new.
//
// GUARD (07 §7.4.11 — never break entry balance for analytics): the split is emitted only when the
// discount reconstructs to a positive cent AND ties back to `net` within a cent; a >=100% or otherwise
// non-reconstructable percentage falls back to the single credit with a caveat. Free-shipping-only
// promos carry no discount pct and take the single-credit path silently. `pct` is a percentage 0..100.
func revenueLines(revenueAccount string, net decimal.Decimal, pct decimal.NullDecimal) ([]entity.AcctJournalLineInsert, string) {
	single := []entity.AcctJournalLineInsert{
		{AccountCode: revenueAccount, Side: entity.AcctSideCredit, Amount: net},
	}
	if net.Sign() <= 0 || !pct.Valid || pct.Decimal.Sign() <= 0 {
		return single, ""
	}
	d := pct.Decimal
	if d.GreaterThanOrEqual(oneHundred) {
		return single, "promo discount >= 100% not reconstructable; 4030 discount line omitted"
	}
	factor := oneHundred.Sub(d) // (100 − pct), strictly in (0,100)
	fullNet := net.Mul(oneHundred).Div(factor).Round(2)
	discount := fullNet.Sub(net) // balancing difference, not independently rounded
	if discount.Sign() <= 0 {
		return single, "" // rounds to nothing — keep the single credit, no caveat
	}
	// Reconstruction must tie back to net within a cent (self-consistency at 2dp).
	if fullNet.Mul(factor).Div(oneHundred).Round(2).Sub(net).Abs().GreaterThan(oneCent) {
		return single, "promo discount did not reconstruct within a cent; 4030 discount line omitted"
	}
	return []entity.AcctJournalLineInsert{
		{AccountCode: revenueAccount, Side: entity.AcctSideCredit, Amount: fullNet},
		{AccountCode: Acc4030, Side: entity.AcctSideDebit, Amount: discount},
	}, ""
}

// grossEUR derives G, the gross EUR amount of an order (04/S1). Priority: the authoritative Stripe
// settlement (total_settled_base) when present — this is the CLAUDE.md "authoritative revenue
// figure", used for any currency; otherwise total_price for a non-Stripe EUR order (custom cash /
// bank-invoice orders never receive a Stripe settlement, and in EUR total_price already is base).
// A Stripe order whose settlement has not arrived is ErrNotReady (the worker retries); a non-Stripe
// non-EUR order cannot be converted without FX and is ErrSkipNonEUR (booked manually). The
// readiness decision is the worker's — this only encodes the amount rule and refuses facts it
// cannot use, keeping k = G/total_price from ever dividing by a bad G.
func grossEUR(f entity.AcctOrderFacts) (decimal.Decimal, error) {
	switch {
	case f.TotalSettledBase.Valid:
		return f.TotalSettledBase.Decimal, nil
	case !isStripe(f.PaymentMethodName) && isBaseCurrency(f.Currency):
		return f.TotalPrice, nil
	case isStripe(f.PaymentMethodName):
		return decimal.Zero, ErrNotReady
	default:
		return decimal.Zero, ErrSkipNonEUR
	}
}

// orderFee is F, the EUR acquirer fee booked on a sale (04/S1). Stripe methods carry a captured
// payment_fee (PaymentFee); NULL / non-positive means no fee line. Non-Stripe methods never have a
// captured fee, so it is estimated from the payment method's fee model (FeePct/FeeFixed, joined
// into the facts) applied to the gross EUR amount g, floored at zero — see BuildOrderSaleEntry's
// caveat for when this estimate is used.
func orderFee(f entity.AcctOrderFacts, g decimal.Decimal) decimal.Decimal {
	if isStripe(f.PaymentMethodName) {
		if f.PaymentFee.Valid && f.PaymentFee.Decimal.IsPositive() {
			return f.PaymentFee.Decimal.Round(2)
		}
		return decimal.Zero
	}
	estimated := g.Mul(f.FeePct).Div(decimal.NewFromInt(100)).Add(f.FeeFixed)
	if estimated.IsNegative() {
		estimated = decimal.Zero
	}
	return estimated.Round(2)
}

// applyCaveats stamps collected caveats onto an entry (has_caveat + joined text, bounded to the
// column width). No caveats leaves the entry untouched (HasCaveat stays false).
func applyCaveats(e *entity.AcctJournalEntryInsert, caveats []string) {
	if len(caveats) == 0 {
		return
	}
	e.HasCaveat = true
	e.Caveat = sql.NullString{String: truncateRunes(strings.Join(caveats, "; "), caveatMaxLen), Valid: true}
}

// joinProductIDs renders a de-duplicated, ascending product-id list for a caveat.
func joinProductIDs(ids []int) string {
	if len(ids) == 0 {
		return ""
	}
	seen := make(map[int]struct{}, len(ids))
	uniq := make([]int, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	sort.Ints(uniq)
	parts := make([]string, len(uniq))
	for i, id := range uniq {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, ", ")
}

// truncateRunes caps s to maxLen characters (runes) without splitting a multi-byte rune.
func truncateRunes(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen])
}

package accounting

import (
	"context"
	"fmt"
	"time"

	acctcalc "github.com/jekabolt/grbpwr-manager/internal/accounting"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// Wave-5 reporting (docs/plan-accounting-phase2/05-wave5-reporting.md §5.1/§5.2): the indirect-method
// Cash Flow statement and the Financial Health ratio set. Both are pure derivations of the ledger:
// GetCashFlowStatement / GetFinancialHealth gather two full-ledger balance snapshots (strictly before
// `from` and before `to`), turn them into period deltas / as-of balances, and hand the numbers to the
// unit-tested pure math in internal/accounting. Money is ledger-first; the ratio report additionally
// borrows operational UNIT counts from metrics (GetSellThroughByDrop), labelled by source. The period
// is [from, to) with `to` exclusive, matching the other period-scoped reports.
//
// Every account is referenced by its Excel-stable code (migrations 0190+; the same literal-code style
// as reports.go / reconcile.go). Codes the plan lists that are not yet seeded (1050 cash, 1230/1240
// capex, 2060 loans) are kept in the code sets as harmless no-ops (they match nothing → zero) so the
// statement is forward-compatible if they are ever added.

// healthUnitsDropLimit bounds GetSellThroughByDrop; it is generous enough to cover every drop cohort so
// the unit totals are the full operational figures (the query aggregates per collection, few rows).
const healthUnitsDropLimit = 100000

// ledgerBalances is a reduced ledger snapshot as of one instant: each account's signed section balance,
// the per-section totals, and the cumulative net profit (Σ_PL of credit − debit). Built by
// ledgerBalancesBefore and consumed as period deltas (closing − opening) or as-of balances.
type ledgerBalances struct {
	byCode    map[string]decimal.Decimal
	bySection map[entity.AcctSection]decimal.Decimal
	netProfit decimal.Decimal
}

// code returns one account's signed balance (zero if the account had no activity).
func (b *ledgerBalances) code(c string) decimal.Decimal { return b.byCode[c] }

// codes sums the signed balances of several accounts.
func (b *ledgerBalances) codes(cs ...string) decimal.Decimal {
	sum := decimal.Zero
	for _, c := range cs {
		sum = sum.Add(b.byCode[c])
	}
	return sum
}

// section returns a section's signed total (zero if empty).
func (b *ledgerBalances) section(s entity.AcctSection) decimal.Decimal { return b.bySection[s] }

// ledgerBalancesBefore reduces every account's journal activity strictly before `before` into a
// ledgerBalances snapshot. Only accounts with activity appear (a missing code reads as zero). The
// signed balance is sectionBalance (the one sign convention shared by every report); net profit is
// Σ over PL lines of (credit − debit), the same expression GetBalanceSheet uses for its virtual
// net-profit row.
func (s *Store) ledgerBalancesBefore(ctx context.Context, before time.Time) (*ledgerBalances, error) {
	type row struct {
		Code      string             `db:"code"`
		Section   entity.AcctSection `db:"section"`
		Statement string             `db:"statement"`
		Debit     decimal.Decimal    `db:"dr"`
		Credit    decimal.Decimal    `db:"cr"`
	}
	rows, err := storeutil.QueryListNamed[row](ctx, s.DB, `
		SELECT a.code, a.section, a.statement,
		       COALESCE(SUM(CASE WHEN l.side = 'debit'  THEN l.amount ELSE 0 END), 0) AS dr,
		       COALESCE(SUM(CASE WHEN l.side = 'credit' THEN l.amount ELSE 0 END), 0) AS cr
		FROM acct_account a
		JOIN acct_journal_line l ON l.account_id = a.id
		JOIN acct_journal_entry e ON e.id = l.entry_id
		WHERE e.occurred_at < :before
		GROUP BY a.code, a.section, a.statement`,
		map[string]any{"before": before.UTC().Format(dateLayout)})
	if err != nil {
		return nil, fmt.Errorf("accounting: ledger balances: %w", err)
	}

	b := &ledgerBalances{
		byCode:    make(map[string]decimal.Decimal, len(rows)),
		bySection: make(map[entity.AcctSection]decimal.Decimal),
	}
	for _, r := range rows {
		bal := sectionBalance(r.Section, r.Debit, r.Credit)
		b.byCode[r.Code] = bal
		b.bySection[r.Section] = b.bySection[r.Section].Add(bal)
		if r.Statement == entity.AcctStatementPL {
			b.netProfit = b.netProfit.Add(r.Credit.Sub(r.Debit))
		}
	}
	return b, nil
}

// GetCashFlowStatement builds the indirect-method Cash Flow statement over [from, to) (§5.1). Net profit
// for the period is add-backed with the non-cash 6370 depreciation charge, then adjusted by the signed
// balance-sheet deltas — working capital and prepayments in operating, capex in investing, equity/draws/
// loans in financing. Cash is 1010+1030(+1050); the Check field (like the Balance Sheet's BalanceCheck)
// compares the derived closing cash to the actual balance as of `to`. The 1225 accumulated-depreciation
// contra is deliberately left out of capex so it cancels the depreciation add-back and cash reconciles.
func (s *Store) GetCashFlowStatement(ctx context.Context, from, to time.Time) (*entity.AcctCashFlowStatement, error) {
	opening, err := s.ledgerBalancesBefore(ctx, from)
	if err != nil {
		return nil, err
	}
	closing, err := s.ledgerBalancesBefore(ctx, to)
	if err != nil {
		return nil, err
	}
	// d is the period delta (closing − opening) of a signed balance across one or more account codes.
	d := func(codes ...string) decimal.Decimal {
		return closing.codes(codes...).Sub(opening.codes(codes...))
	}
	cashCodes := []string{"1010", "1030", "1050"}

	in := acctcalc.CashFlowInputs{
		From:         from,
		To:           to,
		Currency:     cache.GetBaseCurrency(),
		NetProfit:    closing.netProfit.Sub(opening.netProfit),
		Depreciation: d("6370"),

		ARDelta:          d("1040"),
		InventoryDelta:   d("1110", "1120", "1130", "1140"),
		PrepaidDelta:     d("1210"), // current operating asset, not capex
		APDelta:          d("2010"),
		AccruedDelta:     d("2030"),
		IncomeTaxDelta:   d("2050"),
		VatDelta:         d("2070", "2080"),
		PrepaymentsDelta: d("2090"),

		CapexDelta: d("1220", "1230", "1240"),

		EquityDelta: d("3010", "3020"),
		DrawsDelta:  d("3030"),
		LoansDelta:  d("2015", "2060"),

		OpeningCash:       opening.codes(cashCodes...),
		ClosingCashActual: closing.codes(cashCodes...),
	}
	cf := acctcalc.ComputeCashFlow(in)
	if !cf.Balanced {
		cf.Caveats = append(cf.Caveats, "derived closing cash differs from the actual 1010/1030 balance — an account moved outside the modelled cash-flow lines (see Check)")
	}
	return cf, nil
}

// GetFinancialHealth computes the whole ratio set over [from, to) (§5.2). Money figures are ledger-first:
// period P&L (revenue/COGS/net profit, gross revenue and the 4030/4040 contra) and the as-of balance
// sheet come from two ledger snapshots. Operational UNIT counts (units sold/received, active SKU) come
// from metrics.GetSellThroughByDrop — a labelled second-source for units only, never for money. Equity
// includes the current-period retained earnings (the same total as the Balance Sheet).
func (s *Store) GetFinancialHealth(ctx context.Context, from, to time.Time) (*entity.AcctFinancialHealth, error) {
	opening, err := s.ledgerBalancesBefore(ctx, from)
	if err != nil {
		return nil, err
	}
	closing, err := s.ledgerBalancesBefore(ctx, to)
	if err != nil {
		return nil, err
	}
	periodSection := func(sec entity.AcctSection) decimal.Decimal {
		return closing.section(sec).Sub(opening.section(sec))
	}
	periodCodes := func(codes ...string) decimal.Decimal {
		return closing.codes(codes...).Sub(opening.codes(codes...))
	}

	unitsSold, unitsReceived, activeSKU, err := s.financialHealthUnits(ctx, from, to)
	if err != nil {
		return nil, err
	}

	totalAssets := closing.section(entity.AcctSectionAsset)
	totalLiab := closing.section(entity.AcctSectionLiability)

	in := acctcalc.FinancialHealthInputs{
		Currency: cache.GetBaseCurrency(),

		NetRevenue:   periodSection(entity.AcctSectionRevenue),
		GrossRevenue: periodCodes("4010", "4020", "4110", "4310"),
		Cogs:         periodSection(entity.AcctSectionCogs),
		NetProfit:    closing.netProfit.Sub(opening.netProfit),
		Discounts:    periodCodes("4030").Neg(), // contra-revenue signed negative → positive magnitude
		Returns:      periodCodes("4040").Neg(),

		TotalAssets:      totalAssets,
		TotalLiabilities: totalLiab,
		// Equity includes the not-yet-closed current-period profit, matching GetBalanceSheet's TotalEquity.
		Equity: closing.section(entity.AcctSectionEquity).Add(closing.netProfit),
		// Current assets exclude net fixed assets (1220 Equipment net of 1225 Accumulated Depreciation);
		// current liabilities exclude the long-term shareholder loan (2015).
		CurrentAssets:      totalAssets.Sub(closing.codes("1220", "1225")),
		CurrentLiabilities: totalLiab.Sub(closing.code("2015")),
		Cash:               closing.codes("1010", "1030", "1050"),
		AR:                 closing.code("1040"),
		OpeningInventory:   opening.codes("1110", "1120", "1130", "1140"),
		ClosingInventory:   closing.codes("1110", "1120", "1130", "1140"),

		UnitsSold:     unitsSold,
		UnitsReceived: unitsReceived,
		ActiveSKU:     activeSKU,

		// Days in [from, to) — annualizes the flow-over-stock ratios against their yearly benchmarks.
		PeriodDays: int(to.Sub(from).Hours() / 24),
	}
	fh := acctcalc.ComputeFinancialHealth(in)
	fh.From = from
	fh.To = to
	return fh, nil
}

// financialHealthUnits pulls the operational unit totals from metrics.GetSellThroughByDrop (the "units
// from GetMetrics" source, §5.2), summed across every drop cohort: units sold, units received (the
// sold+remaining initial-stock proxy) and the active-SKU count. These are lifetime, collection-tagged
// figures (the query's own scope) — money stays ledger-first; this is a labelled second source for units
// only. `from`/`to` are accepted for signature parity (GetSellThroughByDrop does not window them).
func (s *Store) financialHealthUnits(ctx context.Context, from, to time.Time) (unitsSold, unitsReceived, activeSKU decimal.Decimal, err error) {
	drops, err := s.repo.Metrics().GetSellThroughByDrop(ctx, from, to, healthUnitsDropLimit)
	if err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, fmt.Errorf("accounting: financial health units: %w", err)
	}
	var sold, received, sku int64
	for _, dr := range drops {
		sold += dr.UnitsSold
		received += dr.UnitsBought
		sku += int64(dr.ProductCount)
	}
	return decimal.NewFromInt(sold), decimal.NewFromInt(received), decimal.NewFromInt(sku), nil
}

package accounting

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// Step 7 read-only reports (docs/plan-accounting/06-reports.md): Trial Balance, P&L, Balance Sheet
// and the account drill-down. All are aggregations over acct_journal_line/acct_journal_entry/
// acct_account, running on the caller's connection (s.DB); nothing is cached (admin-only, small
// volumes). GetReconciliation lives in reconcile.go. Money is decimal.Decimal throughout — the
// per-section sign rule (asset/cogs/opex are debit-normal, the rest credit-normal) is applied once
// in Go (sectionBalance) so every report signs balances identically.

// reportMinDate / reportMaxDate are the earliest/latest DATE the reports substitute for an unbounded
// (zero-time) filter edge, mirroring ListJournalEntries.
var (
	reportMinDate = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	reportMaxDate = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
)

// sectionIsDebitNormal reports whether a section's normal balance sits on the debit side
// (asset/cogs/opex/tax). liability/equity/revenue are credit-normal. tax (8010 Corporation Tax) is
// debit-normal like cogs/opex — a tax charge is a debit (phase 2, wave 3).
func sectionIsDebitNormal(section entity.AcctSection) bool {
	switch section {
	case entity.AcctSectionAsset, entity.AcctSectionCogs, entity.AcctSectionOpex, entity.AcctSectionTax:
		return true
	default:
		return false
	}
}

// sectionBalance is the signed closing balance for a section given its debit/credit turnover:
// asset/cogs/opex → dr − cr; liability/equity/revenue → cr − dr (06 §"Знак сальдо"). Used by every
// report so the sign convention lives in exactly one place.
func sectionBalance(section entity.AcctSection, dr, cr decimal.Decimal) decimal.Decimal {
	if sectionIsDebitNormal(section) {
		return dr.Sub(cr)
	}
	return cr.Sub(dr)
}

// runningDelta is how one journal line moves an account's running balance, honouring the account's
// normal side: a debit adds to a debit-normal account and subtracts from a credit-normal one (and
// vice-versa for a credit). Drives the drill-down's running balance.
func runningDelta(section entity.AcctSection, side entity.AcctSide, amount decimal.Decimal) decimal.Decimal {
	debit := side == entity.AcctSideDebit
	if debit == sectionIsDebitNormal(section) {
		return amount
	}
	return amount.Neg()
}

// GetTrialBalance returns per-account debit/credit turnover and the signed closing balance over the
// half-open interval [from, to) (from/to normalised to UTC DATEs), plus the ΣDr/ΣCr totals and the
// balanced invariant. Only accounts with activity in the interval are listed (an all-zero row is
// noise); the totals are unaffected. An INNER join to the entry applies the date filter directly —
// the LEFT-join CTE of 06 would otherwise leak out-of-range line amounts into the sums.
func (s *Store) GetTrialBalance(ctx context.Context, from, to time.Time) (*entity.AcctTrialBalance, error) {
	rows, err := storeutil.QueryListNamed[entity.AcctTrialBalanceRow](ctx, s.DB, `
		SELECT a.code, a.name, a.section, a.statement,
		       COALESCE(SUM(CASE WHEN l.side = 'debit'  THEN l.amount ELSE 0 END), 0) AS dr,
		       COALESCE(SUM(CASE WHEN l.side = 'credit' THEN l.amount ELSE 0 END), 0) AS cr
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		WHERE e.occurred_at >= :from AND e.occurred_at < :to
		GROUP BY a.id, a.code, a.name, a.section, a.statement
		ORDER BY a.code`,
		map[string]any{
			"from": from.UTC().Format(dateLayout),
			"to":   to.UTC().Format(dateLayout),
		})
	if err != nil {
		return nil, fmt.Errorf("accounting: trial balance: %w", err)
	}

	tb := &entity.AcctTrialBalance{From: from, To: to, Rows: make([]entity.AcctTrialBalanceRow, 0, len(rows))}
	for i := range rows {
		rows[i].Balance = sectionBalance(rows[i].Section, rows[i].Debit, rows[i].Credit)
		tb.TotalDebit = tb.TotalDebit.Add(rows[i].Debit)
		tb.TotalCredit = tb.TotalCredit.Add(rows[i].Credit)
		tb.Rows = append(tb.Rows, rows[i])
	}
	tb.Balanced = tb.TotalDebit.Equal(tb.TotalCredit)
	return tb, nil
}

// plTurnoverRow is one PL account's turnover in one month, scanned from the P&L pivot query.
type plTurnoverRow struct {
	Code    string             `db:"code"`
	Name    string             `db:"name"`
	Section entity.AcctSection `db:"section"`
	Month   string             `db:"month"` // 'YYYY-MM-01'
	Debit   decimal.Decimal    `db:"dr"`
	Credit  decimal.Decimal    `db:"cr"`
}

// GetProfitLoss returns the monthly Income Statement over [from, to): one column per calendar month
// of the interval, sections in report order (revenue → cogs → opex), each account signed positive
// in its natural direction (contra accounts 4040/5050 fall out negative on their own), and the
// derived total rows (TotalRevenue, NetCogs, GrossProfit, GrossMarginPct, TotalOpex,
// OperatingProfit, NetMarginPct) — every derived slice aligned one value per month column
// (AcctPLRow carries each account's own YTD in Total). The store's Caveats holds only the dynamic
// count of has_caveat entries in the period; the two permanent phase-1 caveats are injected by the
// apisrv handler (06).
func (s *Store) GetProfitLoss(ctx context.Context, from, to time.Time) (*entity.AcctProfitLoss, error) {
	months := enumerateMonths(from, to)
	monthIdx := make(map[string]int, len(months))
	for i, m := range months {
		monthIdx[m.Format(dateLayout)] = i
	}

	rows, err := storeutil.QueryListNamed[plTurnoverRow](ctx, s.DB, `
		SELECT a.code, a.name, a.section,
		       DATE_FORMAT(e.occurred_at, '%Y-%m-01') AS month,
		       COALESCE(SUM(CASE WHEN l.side = 'debit'  THEN l.amount ELSE 0 END), 0) AS dr,
		       COALESCE(SUM(CASE WHEN l.side = 'credit' THEN l.amount ELSE 0 END), 0) AS cr
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		WHERE a.statement = 'PL' AND e.occurred_at >= :from AND e.occurred_at < :to
		GROUP BY a.id, a.code, a.name, a.section, month`,
		map[string]any{
			"from": from.UTC().Format(dateLayout),
			"to":   to.UTC().Format(dateLayout),
		})
	if err != nil {
		return nil, fmt.Errorf("accounting: profit and loss: %w", err)
	}

	// Pivot into per-section, per-account rows aligned to the month columns. Each section keeps one
	// AcctPLRow per code; codes preserves a stable (later code-sorted) output order.
	sections := map[string]*plSectionAccumulator{
		string(entity.AcctSectionRevenue): {byCode: map[string]*entity.AcctPLRow{}},
		string(entity.AcctSectionCogs):    {byCode: map[string]*entity.AcctPLRow{}},
		string(entity.AcctSectionOpex):    {byCode: map[string]*entity.AcctPLRow{}},
		string(entity.AcctSectionTax):     {byCode: map[string]*entity.AcctPLRow{}},
	}
	// Per-month derived section subtotals.
	totalRevenue := zeroDecimals(len(months))
	netCogs := zeroDecimals(len(months))
	totalOpex := zeroDecimals(len(months))
	totalTax := zeroDecimals(len(months))

	for _, r := range rows {
		idx, ok := monthIdx[r.Month]
		if !ok {
			continue // an entry outside the enumerated columns (should not happen given the WHERE)
		}
		sec := sections[string(r.Section)]
		if sec == nil {
			continue // a PL account in an unexpected section — skip defensively
		}
		row, ok := sec.byCode[r.Code]
		if !ok {
			row = &entity.AcctPLRow{Code: r.Code, Name: r.Name, Values: zeroDecimals(len(months))}
			sec.byCode[r.Code] = row
			sec.codes = append(sec.codes, r.Code)
		}
		val := sectionBalance(r.Section, r.Debit, r.Credit)
		row.Values[idx] = row.Values[idx].Add(val)
		row.Total = row.Total.Add(val)

		switch r.Section {
		case entity.AcctSectionRevenue:
			totalRevenue[idx] = totalRevenue[idx].Add(val)
		case entity.AcctSectionCogs:
			netCogs[idx] = netCogs[idx].Add(val)
		case entity.AcctSectionOpex:
			totalOpex[idx] = totalOpex[idx].Add(val)
		case entity.AcctSectionTax:
			totalTax[idx] = totalTax[idx].Add(val)
		}
	}

	pl := &entity.AcctProfitLoss{
		From:   from,
		To:     to,
		Months: months,
		Sections: []entity.AcctPLSection{
			buildPLSection(entity.AcctSectionRevenue, sections),
			buildPLSection(entity.AcctSectionCogs, sections),
			buildPLSection(entity.AcctSectionOpex, sections),
			buildPLSection(entity.AcctSectionTax, sections),
		},
		TotalRevenue:      totalRevenue,
		NetCogs:           netCogs,
		TotalOpex:         totalOpex,
		TotalTax:          totalTax,
		GrossProfit:       zeroDecimals(len(months)),
		GrossMarginPct:    zeroDecimals(len(months)),
		OperatingProfit:   zeroDecimals(len(months)),
		NetMarginPct:      zeroDecimals(len(months)),
		NetProfitAfterTax: zeroDecimals(len(months)),
	}
	for i := range months {
		gross := totalRevenue[i].Sub(netCogs[i])
		op := gross.Sub(totalOpex[i])
		pl.GrossProfit[i] = gross
		pl.OperatingProfit[i] = op
		pl.GrossMarginPct[i] = marginPct(gross, totalRevenue[i])
		pl.NetMarginPct[i] = marginPct(op, totalRevenue[i])
		// Net Profit after tax = Operating Profit − Σ tax (8010). Tax is a manual journal only, so this
		// equals OperatingProfit until the accountant posts Corporation Tax (phase 2, wave 3).
		pl.NetProfitAfterTax[i] = op.Sub(totalTax[i])
	}

	// C-9: exclude entries that have been reversed (superseded) — counting both the reversed original
	// and its replacement would double-count one caveat. reversed_by IS NULL keeps only live entries.
	caveatCount, err := storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM acct_journal_entry
		WHERE has_caveat = TRUE AND reversed_by IS NULL
		  AND occurred_at >= :from AND occurred_at < :to`,
		map[string]any{
			"from": from.UTC().Format(dateLayout),
			"to":   to.UTC().Format(dateLayout),
		})
	if err != nil {
		return nil, fmt.Errorf("accounting: profit and loss caveat count: %w", err)
	}
	if caveatCount > 0 {
		pl.Caveats = append(pl.Caveats, fmt.Sprintf(
			"%d posted entr%s in this period carr%s a caveat (see reconciliation for unposted operations)",
			caveatCount, plural(caveatCount, "y", "ies"), plural(caveatCount, "ies", "y")))
	}
	return pl, nil
}

// plSectionAccumulator collects one P&L section's rows during the monthly pivot: one AcctPLRow per
// account code, with codes tracking insertion order (sorted before output).
type plSectionAccumulator struct {
	byCode map[string]*entity.AcctPLRow
	codes  []string
}

// buildPLSection materialises one section's rows (code-sorted) into an AcctPLSection.
func buildPLSection(section entity.AcctSection, sections map[string]*plSectionAccumulator) entity.AcctPLSection {
	sec := sections[string(section)]
	out := entity.AcctPLSection{Section: string(section), Rows: make([]entity.AcctPLRow, 0, len(sec.codes))}
	sort.Strings(sec.codes)
	for _, code := range sec.codes {
		out.Rows = append(out.Rows, *sec.byCode[code])
	}
	return out
}

// GetBalanceSheet returns assets/liabilities/equity closing balances from inception through asOf
// (inclusive), each section signed and totalled, plus the virtual "Current Period Net Profit"
// equity row (Σ of every PL account's credit-normal balance over the same span — retained earnings
// this period). BalanceCheck = Assets − (Liabilities + Equity), zero under the Dr==Cr invariant but
// surfaced as the Excel CHK trust panel. Zero-balance accounts are omitted; the NP row is always
// present (apisrv matches it by name).
func (s *Store) GetBalanceSheet(ctx context.Context, asOf time.Time) (*entity.AcctBalanceSheet, error) {
	asOfStr := asOf.UTC().Format(dateLayout)

	type bsRow struct {
		Code    string             `db:"code"`
		Name    string             `db:"name"`
		Section entity.AcctSection `db:"section"`
		Debit   decimal.Decimal    `db:"dr"`
		Credit  decimal.Decimal    `db:"cr"`
	}
	rows, err := storeutil.QueryListNamed[bsRow](ctx, s.DB, `
		SELECT a.code, a.name, a.section,
		       COALESCE(SUM(CASE WHEN l.side = 'debit'  THEN l.amount ELSE 0 END), 0) AS dr,
		       COALESCE(SUM(CASE WHEN l.side = 'credit' THEN l.amount ELSE 0 END), 0) AS cr
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		WHERE a.statement = 'BS' AND e.occurred_at <= :asof
		GROUP BY a.id, a.code, a.name, a.section
		ORDER BY a.code`,
		map[string]any{"asof": asOfStr})
	if err != nil {
		return nil, fmt.Errorf("accounting: balance sheet: %w", err)
	}

	bs := &entity.AcctBalanceSheet{AsOf: asOf}
	bs.Assets.Section = string(entity.AcctSectionAsset)
	bs.Liabilities.Section = string(entity.AcctSectionLiability)
	bs.Equity.Section = string(entity.AcctSectionEquity)

	for _, r := range rows {
		bal := sectionBalance(r.Section, r.Debit, r.Credit)
		if bal.IsZero() {
			continue // an account that netted to zero as of the date is not shown
		}
		row := entity.AcctBalanceSheetRow{Code: r.Code, Name: r.Name, Balance: bal}
		switch r.Section {
		case entity.AcctSectionAsset:
			bs.Assets.Rows = append(bs.Assets.Rows, row)
			bs.Assets.Total = bs.Assets.Total.Add(bal)
		case entity.AcctSectionLiability:
			bs.Liabilities.Rows = append(bs.Liabilities.Rows, row)
			bs.Liabilities.Total = bs.Liabilities.Total.Add(bal)
		case entity.AcctSectionEquity:
			bs.Equity.Rows = append(bs.Equity.Rows, row)
			bs.Equity.Total = bs.Equity.Total.Add(bal)
		}
	}

	// Virtual retained-earnings row: net profit = Σ (credit − debit) over all PL lines to date.
	np, err := storeutil.QueryNamedOne[struct {
		NetProfit decimal.Decimal `db:"np"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(CASE WHEN l.side = 'credit' THEN l.amount ELSE -l.amount END), 0) AS np
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		WHERE a.statement = 'PL' AND e.occurred_at <= :asof`,
		map[string]any{"asof": asOfStr})
	if err != nil {
		return nil, fmt.Errorf("accounting: balance sheet net profit: %w", err)
	}
	bs.Equity.Rows = append(bs.Equity.Rows, entity.AcctBalanceSheetRow{
		Code:    "", // no chart-of-accounts code — a derived line
		Name:    acctNetProfitRowName,
		Balance: np.NetProfit,
	})
	bs.Equity.Total = bs.Equity.Total.Add(np.NetProfit)

	bs.TotalAssets = bs.Assets.Total
	bs.TotalLiabilities = bs.Liabilities.Total
	bs.TotalEquity = bs.Equity.Total
	bs.BalanceCheck = bs.TotalAssets.Sub(bs.TotalLiabilities.Add(bs.TotalEquity))
	bs.Balanced = bs.BalanceCheck.IsZero()
	return bs, nil
}

// acctNetProfitRowName is the exact label the apisrv dto matches to surface GetBalanceSheetResponse.
// net_profit_row (dto/accounting.go: acctNetProfitRowName). Must stay in lockstep with it.
const acctNetProfitRowName = "Current Period Net Profit"

// GetAccountLedger is the drill-down: a paginated statement for one account over [from, to) with a
// running balance. opening_balance (the account's balance strictly before from) comes from one
// query; the running balance is carried in Go from that opening plus the signed delta of every row
// skipped by the page's offset, so a mid-history page continues the balance correctly (06).
func (s *Store) GetAccountLedger(ctx context.Context, code string, f entity.AcctLedgerFilter) (*entity.AcctAccountLedger, error) {
	acc, err := s.getAccountByCode(ctx, code)
	if err != nil {
		return nil, err
	}
	from := f.From
	if from.IsZero() {
		from = reportMinDate
	}
	to := f.To
	if to.IsZero() {
		to = reportMaxDate
	}
	fromStr := from.UTC().Format(dateLayout)
	toStr := to.UTC().Format(dateLayout)

	// Opening balance: everything strictly before `from`.
	opening, err := storeutil.QueryNamedOne[struct {
		Debit  decimal.Decimal `db:"dr"`
		Credit decimal.Decimal `db:"cr"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(CASE WHEN l.side = 'debit'  THEN l.amount ELSE 0 END), 0) AS dr,
		       COALESCE(SUM(CASE WHEN l.side = 'credit' THEN l.amount ELSE 0 END), 0) AS cr
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		WHERE l.account_id = :acc AND e.occurred_at < :from`,
		map[string]any{"acc": acc.Id, "from": fromStr})
	if err != nil {
		return nil, fmt.Errorf("accounting: account ledger opening %s: %w", code, err)
	}
	openingBal := sectionBalance(acc.Section, opening.Debit, opening.Credit)

	// Period turnover + total row count in [from, to).
	period, err := storeutil.QueryNamedOne[struct {
		Debit  decimal.Decimal `db:"dr"`
		Credit decimal.Decimal `db:"cr"`
		Count  int             `db:"cnt"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(CASE WHEN l.side = 'debit'  THEN l.amount ELSE 0 END), 0) AS dr,
		       COALESCE(SUM(CASE WHEN l.side = 'credit' THEN l.amount ELSE 0 END), 0) AS cr,
		       COUNT(*) AS cnt
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		WHERE l.account_id = :acc AND e.occurred_at >= :from AND e.occurred_at < :to`,
		map[string]any{"acc": acc.Id, "from": fromStr, "to": toStr})
	if err != nil {
		return nil, fmt.Errorf("accounting: account ledger period %s: %w", code, err)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	// Running-balance base for this page = opening + Σ delta of the `offset` rows skipped ahead of it.
	pageBase := openingBal
	if offset > 0 {
		prefix, err := storeutil.QueryNamedOne[struct {
			Debit  decimal.Decimal `db:"dr"`
			Credit decimal.Decimal `db:"cr"`
		}](ctx, s.DB, `
			SELECT COALESCE(SUM(CASE WHEN t.side = 'debit'  THEN t.amount ELSE 0 END), 0) AS dr,
			       COALESCE(SUM(CASE WHEN t.side = 'credit' THEN t.amount ELSE 0 END), 0) AS cr
			FROM (
				SELECT l.side, l.amount
				FROM acct_journal_line l
				JOIN acct_journal_entry e ON e.id = l.entry_id
				WHERE l.account_id = :acc AND e.occurred_at >= :from AND e.occurred_at < :to
				ORDER BY e.occurred_at, e.id, l.id
				LIMIT :offset
			) t`,
			map[string]any{"acc": acc.Id, "from": fromStr, "to": toStr, "offset": offset})
		if err != nil {
			return nil, fmt.Errorf("accounting: account ledger prefix %s: %w", code, err)
		}
		pageBase = pageBase.Add(sectionBalance(acc.Section, prefix.Debit, prefix.Credit))
	}

	pageRows, err := storeutil.QueryListNamed[entity.AcctAccountLedgerRow](ctx, s.DB, `
		SELECT e.id, e.occurred_at, e.description, e.source_type, e.source_key,
		       l.side, l.amount, l.note
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		WHERE l.account_id = :acc AND e.occurred_at >= :from AND e.occurred_at < :to
		ORDER BY e.occurred_at, e.id, l.id
		LIMIT :limit OFFSET :offset`,
		map[string]any{"acc": acc.Id, "from": fromStr, "to": toStr, "limit": limit, "offset": offset})
	if err != nil {
		return nil, fmt.Errorf("accounting: account ledger rows %s: %w", code, err)
	}

	running := pageBase
	for i := range pageRows {
		running = running.Add(runningDelta(acc.Section, pageRows[i].Side, pageRows[i].Amount))
		pageRows[i].RunningBalance = running
	}

	return &entity.AcctAccountLedger{
		Code:           acc.Code,
		Name:           acc.Name,
		Section:        acc.Section,
		From:           f.From,
		To:             f.To,
		OpeningBalance: openingBal,
		ClosingBalance: sectionBalance(acc.Section, opening.Debit.Add(period.Debit), opening.Credit.Add(period.Credit)),
		Rows:           pageRows,
		Total:          period.Count,
	}, nil
}

// enumerateMonths returns the 1st-of-month UTC timestamps covering [from, to): every month with any
// occurred_at in the half-open interval. from is snapped down to its 1st; iteration stops at the
// first month not strictly before to.
func enumerateMonths(from, to time.Time) []time.Time {
	start := firstOfMonthUTC(from)
	end := to.UTC()
	var months []time.Time
	for cur := start; cur.Before(end); cur = cur.AddDate(0, 1, 0) {
		months = append(months, cur)
	}
	return months
}

// zeroDecimals returns a slice of n zero decimals (a fresh column vector aligned to the months).
func zeroDecimals(n int) []decimal.Decimal {
	out := make([]decimal.Decimal, n)
	for i := range out {
		out[i] = decimal.Zero
	}
	return out
}

// marginPct is value/base×100 rounded to 2dp, or zero when base is zero (an undefined margin is
// reported as 0, not NaN).
func marginPct(value, base decimal.Decimal) decimal.Decimal {
	if base.IsZero() {
		return decimal.Zero
	}
	return value.Div(base).Mul(decimal.NewFromInt(100)).Round(2)
}

// plural picks the singular or plural form for n (n == 1 → one).
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

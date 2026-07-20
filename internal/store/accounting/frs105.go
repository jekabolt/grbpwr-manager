package accounting

import (
	"context"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	storeutil "github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

type frs105Row struct {
	Code    string             `db:"code"`
	Section entity.AcctSection `db:"section"`
	Debit   decimal.Decimal    `db:"dr"`
	Credit  decimal.Decimal    `db:"cr"`
}

// GetFrs105Accounts re-groups the ledger into FRS 105 micro-entity line items: the Income Statement
// over [from, to) (revenue/cogs/opex activity) and the Statement of Financial Position as at `to`
// (cumulative asset/liability/equity balances). The entity is a single UK Ltd with EUR as its
// functional currency (the Polish operations are part of it, not a separate company), so the
// whole-ledger scope and the EUR figures are both correct for the accounts — a functional-currency
// presentation is permitted for Companies House. It stays a DRAFT for completeness reasons (no tax /
// depreciation accrual) and accountant review, surfaced in the caveats — not for currency or scope.
//
// The SoFP balances by construction: net assets = capital & reserves, where reserves include the
// period's (not-yet-closed) profit, so Σassets − Σliabilities equals equity + profit for the year.
func (s *Store) GetFrs105Accounts(ctx context.Context, from, to time.Time) (*entity.AcctFrs105Accounts, error) {
	toStr := to.UTC().Format(dateLayout)

	// Income statement: per-account turnover over the period.
	plRows, err := storeutil.QueryListNamed[frs105Row](ctx, s.DB, `
		SELECT a.code, a.section,
		       COALESCE(SUM(CASE WHEN l.side = 'debit'  THEN l.amount ELSE 0 END), 0) AS dr,
		       COALESCE(SUM(CASE WHEN l.side = 'credit' THEN l.amount ELSE 0 END), 0) AS cr
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		WHERE e.occurred_at >= :from AND e.occurred_at < :to AND a.statement = 'PL'
		GROUP BY a.code, a.section`,
		map[string]any{"from": from.UTC().Format(dateLayout), "to": toStr})
	if err != nil {
		return nil, fmt.Errorf("accounting: frs105 income: %w", err)
	}

	// Statement of financial position: cumulative balances up to the period end.
	bsRows, err := storeutil.QueryListNamed[frs105Row](ctx, s.DB, `
		SELECT a.code, a.section,
		       COALESCE(SUM(CASE WHEN l.side = 'debit'  THEN l.amount ELSE 0 END), 0) AS dr,
		       COALESCE(SUM(CASE WHEN l.side = 'credit' THEN l.amount ELSE 0 END), 0) AS cr
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		WHERE e.occurred_at < :to AND a.statement = 'BS'
		GROUP BY a.code, a.section`,
		map[string]any{"to": toStr})
	if err != nil {
		return nil, fmt.Errorf("accounting: frs105 position: %w", err)
	}

	r := &entity.AcctFrs105Accounts{From: from, To: to, Currency: cache.GetBaseCurrency()}

	for _, row := range plRows {
		bal := sectionBalance(row.Section, row.Debit, row.Credit)
		switch {
		case row.Section == entity.AcctSectionRevenue:
			r.Turnover = r.Turnover.Add(bal)
		case row.Section == entity.AcctSectionCogs:
			r.CostOfSales = r.CostOfSales.Add(bal)
		case row.Code == "6370": // depreciation — shown separately
			r.Depreciation = r.Depreciation.Add(bal)
		case row.Code == "6360": // taxes — the FRS 105 tax line
			r.Tax = r.Tax.Add(bal)
		default: // remaining opex
			r.AdministrativeExpenses = r.AdministrativeExpenses.Add(bal)
		}
	}
	r.GrossProfit = r.Turnover.Sub(r.CostOfSales)
	r.OperatingProfit = r.GrossProfit.Sub(r.AdministrativeExpenses).Sub(r.Depreciation)
	r.ProfitForYear = r.OperatingProfit.Sub(r.Tax)

	for _, row := range bsRows {
		bal := sectionBalance(row.Section, row.Debit, row.Credit)
		switch row.Section {
		case entity.AcctSectionAsset:
			if row.Code == "1220" || row.Code == "1225" { // equipment net of accumulated depreciation
				r.FixedAssets = r.FixedAssets.Add(bal)
			} else {
				r.CurrentAssets = r.CurrentAssets.Add(bal)
			}
		case entity.AcctSectionLiability:
			r.CreditorsWithinYear = r.CreditorsWithinYear.Add(bal)
		case entity.AcctSectionEquity:
			r.CapitalAndReserves = r.CapitalAndReserves.Add(bal)
		}
	}
	// Reserves include the period's profit, which is not yet closed out of the P&L accounts.
	r.CapitalAndReserves = r.CapitalAndReserves.Add(r.ProfitForYear)
	r.NetCurrentAssets = r.CurrentAssets.Sub(r.CreditorsWithinYear)
	r.TotalAssetsLessCurrentLiab = r.FixedAssets.Add(r.NetCurrentAssets)
	r.NetAssets = r.TotalAssetsLessCurrentLiab.Sub(r.CreditorsAfterYear)

	// The entity is a single UK Ltd whose functional currency is EUR (the Polish operations are part of
	// it, not a separate company), so the whole-ledger scope and the EUR figures are both correct for a
	// Companies House filing — a functional-currency presentation is permitted. The remaining gaps are
	// completeness of the ledger, not structure: a UK CT600 tax computation is filed in GBP separately.
	r.Caveats = []string{
		"prepared in the functional currency (" + r.Currency + ") — permitted for Companies House; a UK CT600 tax computation is filed in GBP",
		"pre-tax — no corporation-tax accrual is posted, so Tax is nil and Profit for the year is stated before tax",
		"no depreciation charge is posted — fixed assets are shown at cost; set a depreciation policy if assets are held",
		"draft for accountant finalisation and review",
	}
	return r, nil
}

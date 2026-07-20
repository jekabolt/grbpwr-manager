package accounting

import (
	"context"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	storeutil "github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// AccrueCorporationTax posts a corporation-tax accrual for the period [from, to): CT = max(0, profit
// before tax) × ratePct, Dr 6365 Corporation Tax / Cr 2075 Corporation Tax Payable. Profit before tax
// is the period's P&L net EXCLUDING the CT account itself (so tax is charged on pre-tax profit). The
// entry is keyed "corp_tax:<from>:<to>", so a second call for the same period is a no-op (returns the
// existing accrual) — to re-accrue after more postings, reverse the prior entry first. Returns the CT
// accrued and whether it was already posted. A loss period (profit ≤ 0) posts nothing.
func (s *Store) AccrueCorporationTax(ctx context.Context, from, to time.Time, ratePct decimal.Decimal) (decimal.Decimal, bool, error) {
	profit, err := storeutil.QueryNamedOne[struct {
		P decimal.Decimal `db:"p"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(CASE WHEN l.side = 'credit' THEN l.amount ELSE -l.amount END), 0) AS p
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		WHERE a.statement = 'PL' AND a.code <> '6365'
		  AND e.occurred_at >= :from AND e.occurred_at < :to`,
		map[string]any{"from": from.UTC().Format(dateLayout), "to": to.UTC().Format(dateLayout)})
	if err != nil {
		return decimal.Zero, false, fmt.Errorf("accounting: corp tax profit: %w", err)
	}

	ct := profit.P.Mul(ratePct).Div(decimal.NewFromInt(100)).Round(2)
	if ct.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, false, nil // loss / no profit → no accrual
	}

	occurred := to.AddDate(0, 0, -1) // last day of the period
	_, dup, err := s.CreateJournalEntry(ctx, entity.AcctJournalEntryInsert{
		OccurredAt: occurred,
		Description: fmt.Sprintf("Corporation tax accrual %s..%s @ %s%%",
			from.Format("2006-01-02"), to.AddDate(0, 0, -1).Format("2006-01-02"), ratePct.String()),
		SourceType: entity.AcctSourceCorpTax,
		SourceKey:  fmt.Sprintf("corp_tax:%s:%s", from.UTC().Format(dateLayout), to.UTC().Format(dateLayout)),
		Lines: []entity.AcctJournalLineInsert{
			{AccountCode: "6365", Side: entity.AcctSideDebit, Amount: ct},
			{AccountCode: "2075", Side: entity.AcctSideCredit, Amount: ct},
		},
	})
	if err != nil {
		return decimal.Zero, false, fmt.Errorf("accounting: post corp tax: %w", err)
	}
	return ct, dup, nil
}

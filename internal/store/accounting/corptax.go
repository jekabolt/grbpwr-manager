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
// before tax) × ratePct, Dr 8010 Corporation Tax / Cr 2050 Income Tax Payable — automating the manual
// CT journal wave 3 defined on those accounts. Profit before tax is the period's P&L net EXCLUDING the
// 'tax' section (so tax is charged on pre-tax profit). The first accrual is keyed
// "corp_tax:<from>:<to>", a re-accrual after a reversal ":vN" (N ≥ 2, mirroring opex/shipping
// versioned keys) — so "reverse the prior entry, then accrue again" actually works: a reversed
// accrual keeps its source_key forever (only reversed_by is set), and without versioning the re-run
// collided with UNIQUE(source_type, source_key) and posted nothing, while postedCorpTax kept
// reporting the reversed (net-zero) figure as if it were still accrued (audit finding 13a).
//
// The returned amount is always the amount ACTUALLY in the ledger, never a freshly-recomputed figure:
// if the period already has an accrual, the existing 8010 debit is returned with alreadyPosted=true —
// even when profit has since moved (more postings) or flipped to a loss, so a stale accrual that now
// needs reversing is still surfaced rather than silently reported as zero. A loss period with no
// existing accrual posts nothing and returns zero.
func (s *Store) AccrueCorporationTax(ctx context.Context, from, to time.Time, ratePct decimal.Decimal) (decimal.Decimal, bool, error) {
	baseKey := fmt.Sprintf("corp_tax:%s:%s", from.UTC().Format(dateLayout), to.UTC().Format(dateLayout))

	// ACTIVE (non-reversed) amount already sitting on the ledger for this period, if any. Read first
	// so both the loss branch and the duplicate branch can report the real figure, not a recompute.
	posted, hasPosted, err := s.postedCorpTax(ctx, baseKey)
	if err != nil {
		return decimal.Zero, false, fmt.Errorf("accounting: corp tax existing: %w", err)
	}

	profit, err := storeutil.QueryNamedOne[struct {
		P decimal.Decimal `db:"p"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(CASE WHEN l.side = 'credit' THEN l.amount ELSE -l.amount END), 0) AS p
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		WHERE a.statement = 'PL' AND a.section <> 'tax'
		  AND e.occurred_at >= :from AND e.occurred_at < :to`,
		map[string]any{"from": from.UTC().Format(dateLayout), "to": to.UTC().Format(dateLayout)})
	if err != nil {
		return decimal.Zero, false, fmt.Errorf("accounting: corp tax profit: %w", err)
	}

	ct := profit.P.Mul(ratePct).Div(decimal.NewFromInt(100)).Round(2)
	if ct.LessThanOrEqual(decimal.Zero) {
		// Loss / no profit: post nothing. If a stale accrual from a previously-profitable state still
		// sits on the ledger, return it (alreadyPosted) so the operator knows to reverse it.
		return posted, hasPosted, nil
	}
	if hasPosted {
		return posted, true, nil // already accrued — the ledger amount, not the recomputed one
	}

	// No ACTIVE accrual — but reversed ones may exist and keep their keys, so take the next
	// version suffix (base key when this is the first accrual ever).
	version, err := s.corpTaxNextVersion(ctx, baseKey)
	if err != nil {
		return decimal.Zero, false, fmt.Errorf("accounting: corp tax version: %w", err)
	}
	key := baseKey
	if version > 1 {
		key = fmt.Sprintf("%s:v%d", baseKey, version)
	}

	occurred := to.AddDate(0, 0, -1) // last day of the period
	_, dup, err := s.CreateJournalEntry(ctx, entity.AcctJournalEntryInsert{
		OccurredAt: occurred,
		Description: fmt.Sprintf("Corporation tax accrual %s..%s @ %s%%",
			from.Format("2006-01-02"), to.AddDate(0, 0, -1).Format("2006-01-02"), ratePct.String()),
		SourceType: entity.AcctSourceCorpTax,
		SourceKey:  key,
		Lines: []entity.AcctJournalLineInsert{
			{AccountCode: "8010", Side: entity.AcctSideDebit, Amount: ct},
			{AccountCode: "2050", Side: entity.AcctSideCredit, Amount: ct},
		},
	})
	if err != nil {
		return decimal.Zero, false, fmt.Errorf("accounting: post corp tax: %w", err)
	}
	if dup {
		// Raced with a concurrent accrual between our read and write — re-read the ledger amount so
		// the response still reflects what is actually posted.
		if amt, ok, rerr := s.postedCorpTax(ctx, baseKey); rerr == nil && ok {
			return amt, true, nil
		}
		return ct, true, nil
	}
	return ct, false, nil
}

// corpTaxNextVersion returns 1 + the number of corp-tax entries EVER posted for the period
// (reversed ones included — they keep their keys), i.e. the next free ':vN' suffix. Runs inside
// the caller's SERIALIZABLE repo.Tx; a concurrent racer computing the same version collides on
// the unique key and lands in the dup branch above.
func (s *Store) corpTaxNextVersion(ctx context.Context, baseKey string) (int, error) {
	n, err := storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM acct_journal_entry
		WHERE source_type = 'corp_tax' AND (source_key = :base OR source_key LIKE :versions)`,
		map[string]any{"base": baseKey, "versions": baseKey + ":v%"})
	if err != nil {
		return 0, err
	}
	return n + 1, nil
}

// postedCorpTax returns the corporation tax ACTIVELY accrued for a period (the 8010 debit across the
// period's non-reversed corp_tax entries, base key or any ':vN' version), and whether such an entry
// exists. Reversed accruals are excluded — after a reversal the period reads as "not accrued", which
// is what lets AccrueCorporationTax post a fresh versioned entry (audit finding 13a); before, the
// reversed entry still matched and the stale figure was reported as if it were accrued.
func (s *Store) postedCorpTax(ctx context.Context, baseKey string) (decimal.Decimal, bool, error) {
	row, err := storeutil.QueryNamedOne[struct {
		Amt   decimal.Decimal `db:"amt"`
		Found int             `db:"found"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(l.amount), 0) AS amt, COUNT(*) AS found
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		WHERE e.source_type = 'corp_tax'
		  AND (e.source_key = :base OR e.source_key LIKE :versions)
		  AND e.reversed_by IS NULL
		  AND a.code = '8010' AND l.side = 'debit'`,
		map[string]any{"base": baseKey, "versions": baseKey + ":v%"})
	if err != nil {
		return decimal.Zero, false, err
	}
	return row.Amt, row.Found > 0, nil
}

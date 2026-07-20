package accounting

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ValidateBalanced checks that a built entry is a well-formed double-entry posting: at least one
// line, every amount strictly positive (the side carries the sign, mirroring chk_acct_line_amount),
// a valid debit/credit side on each line, and total debits exactly equal to total credits. The
// store's CreateJournalEntry enforces the same invariant before insert; this is the pure-side check
// the builders' tests assert on every case (including randomised proportions) so a rounding or
// balancing-line bug cannot slip through.
func ValidateBalanced(e entity.AcctJournalEntryInsert) error {
	if len(e.Lines) == 0 {
		return fmt.Errorf("entry %q/%q has no lines", e.SourceType, e.SourceKey)
	}
	debit := decimal.Zero
	credit := decimal.Zero
	for i, l := range e.Lines {
		if !l.Amount.IsPositive() {
			return fmt.Errorf("line %d (%s): amount must be > 0, got %s", i, l.AccountCode, l.Amount.String())
		}
		switch l.Side {
		case entity.AcctSideDebit:
			debit = debit.Add(l.Amount)
		case entity.AcctSideCredit:
			credit = credit.Add(l.Amount)
		default:
			return fmt.Errorf("line %d (%s): invalid side %q", i, l.AccountCode, l.Side)
		}
	}
	if !debit.Equal(credit) {
		return fmt.Errorf("unbalanced entry %q/%q: debit %s != credit %s",
			e.SourceType, e.SourceKey, debit.String(), credit.String())
	}
	return nil
}

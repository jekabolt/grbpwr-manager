package accounting

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// BuildOpexMonthEntry builds the opex_month journal entry for a month's operating expenses (rule
// O1, docs/plan-accounting/04-posting-rules.md). Input is the month's costed OPEX (amount_base NOT
// NULL) already grouped by category; each category becomes a debit to its P&L account and the whole
// accrual is credited to 2030 Accrued Expenses (the balancing line = the sum of the debits).
//
// source_key is 'YYYY-MM', or 'YYYY-MM:vN' when version > 1 (a repost supersedes the prior version
// — the worker computes the version). occurred_at is the last day of the month. An unknown category
// is booked to 6390 with a caveat; per-category uncosted (NULL-base) line labels are echoed into
// caveats. An empty / all-zero month posts nothing (ErrSkipEmpty) — the worker reverses any prior
// version instead.
func BuildOpexMonthEntry(month time.Time, sums []entity.AcctOpexCategorySum, version int) (entity.AcctJournalEntryInsert, error) {
	var caveats []string
	var lines []entity.AcctJournalLineInsert
	total := decimal.Zero

	for _, s := range sums {
		if len(s.UncostedLabels) > 0 {
			caveats = append(caveats, fmt.Sprintf("opex line(s) skipped (no base amount) in %q: %s",
				s.Category, strings.Join(s.UncostedLabels, ", ")))
		}
		amount := s.AmountBase.Round(2)
		if !amount.IsPositive() {
			continue
		}
		code, known := OpexCategoryAccount(s.Category)
		if !known {
			caveats = append(caveats, fmt.Sprintf("unknown opex category %q booked to %s", s.Category, Acc6390))
		}
		lines = append(lines, entity.AcctJournalLineInsert{
			AccountCode: code,
			Side:        entity.AcctSideDebit,
			Amount:      amount,
			Note:        sql.NullString{String: s.Category, Valid: s.Category != ""},
		})
		total = total.Add(amount)
	}

	if len(lines) == 0 || !total.IsPositive() {
		return entity.AcctJournalEntryInsert{}, ErrSkipEmpty
	}

	// Balancing credit: the whole month accrued.
	lines = append(lines, entity.AcctJournalLineInsert{
		AccountCode: Acc2030,
		Side:        entity.AcctSideCredit,
		Amount:      total,
	})

	entry := entity.AcctJournalEntryInsert{
		OccurredAt:  monthEnd(month),
		Description: fmt.Sprintf("opex accrual %s", month.Format("2006-01")),
		SourceType:  entity.AcctSourceOpexMonth,
		SourceKey:   opexSourceKey(month, version),
		CreatedBy:   createdBySystem,
		Lines:       lines,
	}
	applyCaveats(&entry, caveats)
	return entry, nil
}

// opexSourceKey is 'YYYY-MM' for the first version of a month, 'YYYY-MM:vN' for a repost (N > 1).
func opexSourceKey(month time.Time, version int) string {
	key := month.Format("2006-01")
	if version > 1 {
		return fmt.Sprintf("%s:v%d", key, version)
	}
	return key
}

// monthEnd returns the last calendar day of month's month at 00:00 UTC — the accrual's occurred_at.
// It uses month's own calendar year/month (not month.UTC(), which could roll a tz-offset value into
// the previous month).
func monthEnd(month time.Time) time.Time {
	firstOfMonth := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	return firstOfMonth.AddDate(0, 1, -1)
}

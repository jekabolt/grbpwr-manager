package accounting

import (
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// BuildDevExpenseEntry builds the dev_expense journal entry for one tech_card_dev_expense row (phase 2,
// wave 3, feature 3.2 — docs/plan-accounting-phase2/03-wave3-pnl-completion.md §3.2). Development / R&D
// spend is booked as a 6210 Samples & Prototyping charge: Dr 6210 / Cr 2030 Accrued Expenses.
//
// 6210 is the SAME account material issue_sample debits (M4), but there is no double count: a dev
// expense is a MANUAL cost accrual (money out via 2030), while an issue_sample moves already-capitalised
// material out of 1110 — different credit legs of the one 6210 R&D group (recorded here for the 09 doc).
//
// amount_base NULL means the row is uncosted (no base conversion yet) — ErrSkipUncosted, the phase-1
// standard (surfaced by reconciliation, never invented). A row that is edited or deleted reposts via a
// versioned source_key: 'dev:<id>' first, 'dev:<id>:vN' for a repost (mirrors opex). occurred_at is
// incurred_at (fallback: created_at).
func BuildDevExpenseEntry(f entity.AcctDevExpenseFacts, version int) (entity.AcctJournalEntryInsert, error) {
	if !f.AmountBase.Valid {
		return entity.AcctJournalEntryInsert{}, ErrSkipUncosted
	}
	amount := f.AmountBase.Decimal.Round(2)
	if amount.Sign() <= 0 {
		return entity.AcctJournalEntryInsert{}, ErrSkipEmpty
	}

	lines := []entity.AcctJournalLineInsert{
		{AccountCode: Acc6210, Side: entity.AcctSideDebit, Amount: amount, Note: nullStr(f.Kind)},
		{AccountCode: Acc2030, Side: entity.AcctSideCredit, Amount: amount},
	}

	entry := entity.AcctJournalEntryInsert{
		OccurredAt:  devExpenseOccurredAt(f),
		Description: devExpenseDescription(f),
		SourceType:  entity.AcctSourceDevExpense,
		SourceKey:   devExpenseSourceKey(f.Id, version),
		CreatedBy:   createdBySystem,
		Lines:       lines,
	}
	return entry, nil
}

// devExpenseDescription renders a bounded, human-readable description for the entry.
func devExpenseDescription(f entity.AcctDevExpenseFacts) string {
	desc := fmt.Sprintf("dev expense %s (%s)", f.TechCardName, f.Kind)
	if f.Description.Valid && f.Description.String != "" {
		desc = fmt.Sprintf("%s — %s", desc, f.Description.String)
	}
	return truncateRunes(desc, descMaxLen)
}

// devExpenseOccurredAt is the row's posting instant: incurred_at when set, else created_at.
func devExpenseOccurredAt(f entity.AcctDevExpenseFacts) time.Time {
	if f.IncurredAt.Valid {
		return f.IncurredAt.Time
	}
	return f.CreatedAt
}

// devExpenseSourceKey is 'dev:<id>' for the first version, 'dev:<id>:vN' for a repost (N > 1).
func devExpenseSourceKey(id, version int) string {
	if version > 1 {
		return fmt.Sprintf("dev:%d:v%d", id, version)
	}
	return fmt.Sprintf("dev:%d", id)
}

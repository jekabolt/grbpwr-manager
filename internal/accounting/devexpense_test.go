package accounting

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testIncurred = time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)

func devFacts(amountBase string) entity.AcctDevExpenseFacts {
	f := entity.AcctDevExpenseFacts{
		Id:           7,
		TechCardID:   3,
		TechCardName: "Jacket",
		Kind:         "sample",
		Description:  sql.NullString{String: "first proto", Valid: true},
		IncurredAt:   sql.NullTime{Time: testIncurred, Valid: true},
		CreatedAt:    testIncurred.Add(24 * time.Hour),
	}
	if amountBase != "" {
		f.AmountBase = nd(amountBase)
	}
	return f
}

func TestBuildDevExpenseEntry_Basic(t *testing.T) {
	e, err := BuildDevExpenseEntry(devFacts("250.00"), 1)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))

	assert.Equal(t, entity.AcctSourceDevExpense, e.SourceType)
	assert.Equal(t, "dev:7", e.SourceKey)
	assert.Equal(t, testIncurred, e.OccurredAt)
	assert.False(t, e.HasCaveat)
	assertAmount(t, e, Acc6210, entity.AcctSideDebit, "250.00")
	assertAmount(t, e, Acc2030, entity.AcctSideCredit, "250.00")
	assert.Contains(t, e.Description, "Jacket")
	assert.Contains(t, e.Description, "sample")
	// kind travels onto the 6210 line note.
	for _, l := range e.Lines {
		if l.AccountCode == Acc6210 {
			assert.Equal(t, "sample", l.Note.String)
		}
	}
}

func TestBuildDevExpenseEntry_Version(t *testing.T) {
	e, err := BuildDevExpenseEntry(devFacts("100.00"), 2)
	require.NoError(t, err)
	assert.Equal(t, "dev:7:v2", e.SourceKey)
}

func TestBuildDevExpenseEntry_UncostedSkips(t *testing.T) {
	_, err := BuildDevExpenseEntry(devFacts(""), 1) // amount_base NULL
	assert.ErrorIs(t, err, ErrSkipUncosted)
}

func TestBuildDevExpenseEntry_ZeroSkips(t *testing.T) {
	_, err := BuildDevExpenseEntry(devFacts("0.00"), 1)
	assert.ErrorIs(t, err, ErrSkipEmpty)
}

func TestBuildDevExpenseEntry_IncurredFallback(t *testing.T) {
	f := devFacts("50.00")
	f.IncurredAt = sql.NullTime{} // no incurred date -> created_at
	e, err := BuildDevExpenseEntry(f, 1)
	require.NoError(t, err)
	assert.Equal(t, f.CreatedAt, e.OccurredAt)
}

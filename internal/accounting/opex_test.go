package accounting

import (
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testMonth = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

func TestBuildOpexMonthEntry_Basic(t *testing.T) {
	sums := []entity.AcctOpexCategorySum{
		{Category: "salaries", AmountBase: dec("5000.00")},
		{Category: "rent", AmountBase: dec("1200.00")},
	}
	e, err := BuildOpexMonthEntry(testMonth, sums, 1)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))

	assert.Equal(t, entity.AcctSourceOpexMonth, e.SourceType)
	assert.Equal(t, "2026-06", e.SourceKey)
	assert.Equal(t, time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC), e.OccurredAt)
	assert.False(t, e.HasCaveat)

	assertAmount(t, e, Acc6330, entity.AcctSideDebit, "5000.00")
	assertAmount(t, e, Acc6340, entity.AcctSideDebit, "1200.00")
	assertAmount(t, e, Acc2030, entity.AcctSideCredit, "6200.00")
}

func TestBuildOpexMonthEntry_Cases(t *testing.T) {
	t.Run("unknown category books to 6390 with caveat", func(t *testing.T) {
		sums := []entity.AcctOpexCategorySum{{Category: "crypto_mining", AmountBase: dec("42.00")}}
		e, err := BuildOpexMonthEntry(testMonth, sums, 1)
		require.NoError(t, err)
		require.NoError(t, ValidateBalanced(e))
		assertAmount(t, e, Acc6390, entity.AcctSideDebit, "42.00")
		assert.True(t, e.HasCaveat)
		assert.Contains(t, e.Caveat.String, "unknown opex category")
	})

	t.Run("uncosted line labels surfaced", func(t *testing.T) {
		sums := []entity.AcctOpexCategorySum{
			{Category: "software", AmountBase: dec("300.00"), UncostedLabels: []string{"Figma", "Adobe"}},
		}
		e, err := BuildOpexMonthEntry(testMonth, sums, 1)
		require.NoError(t, err)
		require.NoError(t, ValidateBalanced(e))
		assert.True(t, e.HasCaveat)
		assert.Contains(t, e.Caveat.String, "Figma")
		assert.Contains(t, e.Caveat.String, "Adobe")
	})

	t.Run("repost version suffixes source key", func(t *testing.T) {
		sums := []entity.AcctOpexCategorySum{{Category: "rent", AmountBase: dec("1000.00")}}
		e, err := BuildOpexMonthEntry(testMonth, sums, 3)
		require.NoError(t, err)
		assert.Equal(t, "2026-06:v3", e.SourceKey)
	})

	t.Run("note carries the category", func(t *testing.T) {
		sums := []entity.AcctOpexCategorySum{{Category: "rent", AmountBase: dec("1000.00")}}
		e, err := BuildOpexMonthEntry(testMonth, sums, 1)
		require.NoError(t, err)
		for _, l := range e.Lines {
			if l.AccountCode == Acc6340 {
				assert.Equal(t, "rent", l.Note.String)
				assert.True(t, l.Note.Valid)
			}
		}
	})
}

func TestBuildOpexMonthEntry_Empty(t *testing.T) {
	t.Run("no sums", func(t *testing.T) {
		_, err := BuildOpexMonthEntry(testMonth, nil, 1)
		assert.ErrorIs(t, err, ErrSkipEmpty)
	})
	t.Run("all-zero sums", func(t *testing.T) {
		sums := []entity.AcctOpexCategorySum{
			{Category: "rent", AmountBase: dec("0")},
			{Category: "salaries", AmountBase: dec("0")},
		}
		_, err := BuildOpexMonthEntry(testMonth, sums, 1)
		assert.ErrorIs(t, err, ErrSkipEmpty)
	})
}

func TestMonthEnd(t *testing.T) {
	tests := []struct {
		month time.Time
		want  time.Time
	}{
		{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)},
		{time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)},
		{time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)}, // leap
		{time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, monthEnd(tt.month))
	}
}

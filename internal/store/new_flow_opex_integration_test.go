package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// month returns the 1st of the given year/month at 00:00 UTC (the OPEX month key).
func opexMonth(year int, m time.Month) time.Time {
	return time.Date(year, m, 1, 0, 0, 0, 0, time.UTC)
}

// TestOpexV2 exercises new-flow NF-08: the opex_line journal (multi-currency, folded to base),
// the legacy-aggregate mirror, recurring-template materialisation (idempotent, insert-only,
// archive stops it, uncosted when no FX rate), and the dashboard operating-result total/caveat.
// Uses a distinctive 2029 month range and cleans its rows so it doesn't disturb other suites.
func TestOpexV2(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	t.Cleanup(func() {
		_, _ = testDB.ExecContext(ctx, "DELETE FROM opex_line WHERE label LIKE 'NF-OPEX-TEST%' OR month >= '2029-01-01'")
		_, _ = testDB.ExecContext(ctx, "DELETE FROM opex_recurring WHERE label LIKE 'NF-OPEX-TEST%'")
	})

	mtr := s.Metrics()
	nd := func(str string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(str), Valid: true}
	}

	// --- manual lines: one base-EUR, one uncosted (no amount_base) ---
	require.NoError(t, mtr.UpsertOpexLines(ctx, []entity.OpexLineInsert{
		{
			Month: opexMonth(2029, time.June), Category: "software", Label: "NF-OPEX-TEST Adobe",
			Amount: decimal.RequireFromString("60"), Currency: "USD", AmountBase: nd("54"), // folded by handler
		},
		{
			Month: opexMonth(2029, time.June), Category: "software", Label: "NF-OPEX-TEST Figma",
			Amount: decimal.RequireFromString("15"), Currency: "GBP", // uncosted: no AmountBase
		},
	}))

	lines, err := mtr.ListOpexLines(ctx, entity.OpexLineFilter{
		MonthFrom: opexMonth(2029, time.June), MonthTo: opexMonth(2029, time.June), Category: "software",
	})
	require.NoError(t, err)
	require.Len(t, lines, 2)
	byLabel := map[string]entity.OpexLine{}
	for _, l := range lines {
		byLabel[l.Label] = l
	}
	require.True(t, byLabel["NF-OPEX-TEST Adobe"].AmountBase.Valid)
	require.Equal(t, "54", byLabel["NF-OPEX-TEST Adobe"].AmountBase.Decimal.String())
	require.False(t, byLabel["NF-OPEX-TEST Figma"].AmountBase.Valid, "GBP line has no rate → uncosted")

	// upsert on (month,category,label) updates in place, not duplicates.
	require.NoError(t, mtr.UpsertOpexLines(ctx, []entity.OpexLineInsert{{
		Month: opexMonth(2029, time.June), Category: "software", Label: "NF-OPEX-TEST Adobe",
		Amount: decimal.RequireFromString("70"), Currency: "USD", AmountBase: nd("63"),
	}}))
	lines, err = mtr.ListOpexLines(ctx, entity.OpexLineFilter{
		MonthFrom: opexMonth(2029, time.June), MonthTo: opexMonth(2029, time.June), Category: "software",
	})
	require.NoError(t, err)
	require.Len(t, lines, 2, "upsert must not create a duplicate")

	// delete one line.
	require.NoError(t, mtr.DeleteOpexLine(ctx, byLabel["NF-OPEX-TEST Figma"].Id))
	lines, err = mtr.ListOpexLines(ctx, entity.OpexLineFilter{
		MonthFrom: opexMonth(2029, time.June), MonthTo: opexMonth(2029, time.June),
	})
	require.NoError(t, err)
	require.Len(t, lines, 1)

	// --- legacy aggregate mirrors into opex_line as '(aggregate)' ---
	require.NoError(t, mtr.UpsertOpexEntries(ctx, []entity.OpexEntry{{
		Month: opexMonth(2029, time.July), Category: "rent", Amount: decimal.RequireFromString("1200"),
	}}))
	agg, err := mtr.ListOpexLines(ctx, entity.OpexLineFilter{
		MonthFrom: opexMonth(2029, time.July), MonthTo: opexMonth(2029, time.July), Category: "rent",
	})
	require.NoError(t, err)
	require.Len(t, agg, 1)
	require.Equal(t, "(aggregate)", agg[0].Label)
	require.True(t, agg[0].AmountBase.Valid)
	require.Equal(t, "1200", agg[0].AmountBase.Decimal.String())

	// --- recurring template materialisation ---
	rates := map[string]decimal.Decimal{"USD": decimal.RequireFromString("0.9")}
	recID, err := mtr.UpsertOpexRecurring(ctx, entity.OpexRecurringInsert{
		Label: "NF-OPEX-TEST Salary", Category: "salaries",
		Amount: decimal.RequireFromString("1000"), Currency: "USD",
		ActiveFrom: opexMonth(2029, time.January),
	}, 0)
	require.NoError(t, err)
	require.Positive(t, recID)

	// materialise Jan..Mar → 3 lines, each folded 1000 * 0.9 = 900.
	n, err := mtr.MaterializeOpexRecurring(ctx, opexMonth(2029, time.March), rates)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	sal, err := mtr.ListOpexLines(ctx, entity.OpexLineFilter{
		MonthFrom: opexMonth(2029, time.January), MonthTo: opexMonth(2029, time.March), Category: "salaries",
	})
	require.NoError(t, err)
	require.Len(t, sal, 3)
	for _, l := range sal {
		require.Equal(t, int32(recID), l.RecurringId.Int32)
		require.True(t, l.AmountBase.Valid)
		require.Equal(t, "900", l.AmountBase.Decimal.String())
	}

	// re-running the same window is idempotent (insert-only).
	n, err = mtr.MaterializeOpexRecurring(ctx, opexMonth(2029, time.March), rates)
	require.NoError(t, err)
	require.Equal(t, 0, n, "re-materialise must book nothing new")

	// advancing the horizon books only the new month.
	n, err = mtr.MaterializeOpexRecurring(ctx, opexMonth(2029, time.April), rates)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// editing the template amount must NOT rewrite an already-booked month.
	_, err = mtr.UpsertOpexRecurring(ctx, entity.OpexRecurringInsert{
		Label: "NF-OPEX-TEST Salary", Category: "salaries",
		Amount: decimal.RequireFromString("5000"), Currency: "USD",
		ActiveFrom: opexMonth(2029, time.January),
	}, recID)
	require.NoError(t, err)
	n, err = mtr.MaterializeOpexRecurring(ctx, opexMonth(2029, time.April), rates)
	require.NoError(t, err)
	require.Equal(t, 0, n)
	jan, err := mtr.ListOpexLines(ctx, entity.OpexLineFilter{
		MonthFrom: opexMonth(2029, time.January), MonthTo: opexMonth(2029, time.January), Category: "salaries",
	})
	require.NoError(t, err)
	require.Len(t, jan, 1)
	require.Equal(t, "900", jan[0].AmountBase.Decimal.String(), "past booking is frozen at the old amount")

	// no FX rate → materialised line is uncosted (amount_base NULL).
	_, err = mtr.MaterializeOpexRecurring(ctx, opexMonth(2029, time.April), map[string]decimal.Decimal{})
	require.NoError(t, err)
	// archiving stops further materialisation.
	require.NoError(t, mtr.ArchiveOpexRecurring(ctx, recID))
	n, err = mtr.MaterializeOpexRecurring(ctx, opexMonth(2029, time.December), rates)
	require.NoError(t, err)
	require.Equal(t, 0, n, "archived template materialises nothing")

	// ListOpexRecurring: active-only hides the archived template; includeArchived shows it.
	active, err := mtr.ListOpexRecurring(ctx, false)
	require.NoError(t, err)
	for _, r := range active {
		require.NotEqual(t, recID, r.Id, "archived template must be hidden from active list")
	}
	all, err := mtr.ListOpexRecurring(ctx, true)
	require.NoError(t, err)
	found := false
	for _, r := range all {
		if r.Id == recID {
			found = true
			require.True(t, r.Archived)
		}
	}
	require.True(t, found)

	// --- dashboard operating result reads opex_line; uncosted lines set the caveat ---
	// August 2029: one costed line (100 EUR) + one uncosted line → total counts 100, caveat set.
	require.NoError(t, mtr.UpsertOpexLines(ctx, []entity.OpexLineInsert{
		{Month: opexMonth(2029, time.August), Category: "other", Label: "NF-OPEX-TEST costed",
			Amount: decimal.RequireFromString("100"), Currency: "EUR", AmountBase: nd("100")},
		{Month: opexMonth(2029, time.August), Category: "other", Label: "NF-OPEX-TEST uncosted",
			Amount: decimal.RequireFromString("50"), Currency: "GBP"},
	}))
	from := opexMonth(2029, time.August)
	to := opexMonth(2029, time.September)
	dash, err := mtr.GetDashboard(ctx, from, to, 10)
	require.NoError(t, err)
	require.Equal(t, "100", dash.OpexTotal.String(), "uncosted line excluded from the total")
	require.NotEmpty(t, dash.OpexCaveat, "uncosted line must flag the operating result")
}

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
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM opex_line WHERE label LIKE 'NF-OPEX-TEST%' OR month >= '2029-01-01'")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM opex_recurring WHERE label LIKE 'NF-OPEX-TEST%'")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM costing_fx_rate WHERE currency IN ('TSD','TSJ')")
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
	// Materialisation now folds each month at the FX rate effective THAT month, read from
	// costing_fx_rate (no rates param). Seed test-only currencies so we don't perturb real rates
	// other suites read: TSD = 0.9 from 2028 (bumped to 0.8 from 2029-07 for the per-month test),
	// TSJ seeded later to prove recost of an initially-uncosted line.
	seedRate := func(cur, rate string, y int, m time.Month) {
		require.NoError(t, s.TechCards().UpsertCostingFxRates(ctx, []entity.CostingFxRate{{
			Currency: cur, RateToBase: decimal.RequireFromString(rate), ValidFrom: opexMonth(y, m),
		}}))
	}
	seedRate("TSD", "0.9", 2028, time.January)

	recID, err := mtr.UpsertOpexRecurring(ctx, entity.OpexRecurringInsert{
		Label: "NF-OPEX-TEST Salary", Category: "salaries",
		Amount: decimal.RequireFromString("1000"), Currency: "TSD",
		ActiveFrom: opexMonth(2029, time.January),
	}, 0)
	require.NoError(t, err)
	require.Positive(t, recID)

	// materialise Jan..Mar → 3 lines, each folded 1000 * 0.9 = 900.
	n, err := mtr.MaterializeOpexRecurring(ctx, opexMonth(2029, time.March))
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

	// re-running the same window is idempotent (books nothing new).
	n, err = mtr.MaterializeOpexRecurring(ctx, opexMonth(2029, time.March))
	require.NoError(t, err)
	require.Equal(t, 0, n, "re-materialise must book nothing new")

	// advancing the horizon books only the new month.
	n, err = mtr.MaterializeOpexRecurring(ctx, opexMonth(2029, time.April))
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// editing the template amount must NOT rewrite an already-booked month.
	_, err = mtr.UpsertOpexRecurring(ctx, entity.OpexRecurringInsert{
		Label: "NF-OPEX-TEST Salary", Category: "salaries",
		Amount: decimal.RequireFromString("5000"), Currency: "TSD",
		ActiveFrom: opexMonth(2029, time.January),
	}, recID)
	require.NoError(t, err)
	n, err = mtr.MaterializeOpexRecurring(ctx, opexMonth(2029, time.April))
	require.NoError(t, err)
	require.Equal(t, 0, n)
	jan, err := mtr.ListOpexLines(ctx, entity.OpexLineFilter{
		MonthFrom: opexMonth(2029, time.January), MonthTo: opexMonth(2029, time.January), Category: "salaries",
	})
	require.NoError(t, err)
	require.Len(t, jan, 1)
	require.Equal(t, "900", jan[0].AmountBase.Decimal.String(), "past booking is frozen at the old amount")

	// renaming a template must NOT double-book past months: dedup is (recurring_id, month), so the
	// Jan..Apr rows are updated in place (label unchanged — as-booked), not duplicated (nf08-01).
	_, err = mtr.UpsertOpexRecurring(ctx, entity.OpexRecurringInsert{
		Label: "NF-OPEX-TEST Salary Renamed", Category: "salaries",
		Amount: decimal.RequireFromString("5000"), Currency: "TSD",
		ActiveFrom: opexMonth(2029, time.January),
	}, recID)
	require.NoError(t, err)
	n, err = mtr.MaterializeOpexRecurring(ctx, opexMonth(2029, time.April))
	require.NoError(t, err)
	require.Equal(t, 0, n, "rename must not re-book past months")
	salAfter, err := mtr.ListOpexLines(ctx, entity.OpexLineFilter{
		MonthFrom: opexMonth(2029, time.January), MonthTo: opexMonth(2029, time.April), Category: "salaries",
	})
	require.NoError(t, err)
	require.Len(t, salAfter, 4, "4 months, one row each — no label-driven duplicates")

	// two distinct templates sharing (category, label) both book every month (nf08-02): the second
	// must NOT be silently dropped. Use a fresh category/month so it doesn't disturb the salary rows.
	dupA, err := mtr.UpsertOpexRecurring(ctx, entity.OpexRecurringInsert{
		Label: "NF-OPEX-TEST Seamstress", Category: "wages",
		Amount: decimal.RequireFromString("100"), Currency: "TSD", ActiveFrom: opexMonth(2029, time.May),
	}, 0)
	require.NoError(t, err)
	dupB, err := mtr.UpsertOpexRecurring(ctx, entity.OpexRecurringInsert{
		Label: "NF-OPEX-TEST Seamstress", Category: "wages",
		Amount: decimal.RequireFromString("200"), Currency: "TSD", ActiveFrom: opexMonth(2029, time.May),
	}, 0)
	require.NoError(t, err)
	require.NotEqual(t, dupA, dupB)
	_, err = mtr.MaterializeOpexRecurring(ctx, opexMonth(2029, time.May))
	require.NoError(t, err)
	// the proof of nf08-02: BOTH same-(category,label) templates booked their own May line — the
	// second is no longer silently swallowed by a label-based unique key.
	wages, err := mtr.ListOpexLines(ctx, entity.OpexLineFilter{
		MonthFrom: opexMonth(2029, time.May), MonthTo: opexMonth(2029, time.May), Category: "wages",
	})
	require.NoError(t, err)
	require.Len(t, wages, 2)
	require.NotEqual(t, wages[0].RecurringId.Int32, wages[1].RecurringId.Int32, "one line per template")

	// recost (nf08-03): a template whose currency has NO rate books uncosted (amount_base NULL);
	// once a rate is added, a later tick recosts that same month in place (past base is not frozen
	// while it is NULL). Contractor 200 TSJ, active June.
	contractorID, err := mtr.UpsertOpexRecurring(ctx, entity.OpexRecurringInsert{
		Label: "NF-OPEX-TEST Contractor", Category: "services",
		Amount: decimal.RequireFromString("200"), Currency: "TSJ", ActiveFrom: opexMonth(2029, time.June),
	}, 0)
	require.NoError(t, err)
	_, err = mtr.MaterializeOpexRecurring(ctx, opexMonth(2029, time.June))
	require.NoError(t, err)
	svc, err := mtr.ListOpexLines(ctx, entity.OpexLineFilter{
		MonthFrom: opexMonth(2029, time.June), MonthTo: opexMonth(2029, time.June), Category: "services",
	})
	require.NoError(t, err)
	require.Len(t, svc, 1)
	require.False(t, svc[0].AmountBase.Valid, "no TSJ rate yet → uncosted")

	seedRate("TSJ", "0.5", 2028, time.January)
	_, err = mtr.MaterializeOpexRecurring(ctx, opexMonth(2029, time.June))
	require.NoError(t, err)
	svc, err = mtr.ListOpexLines(ctx, entity.OpexLineFilter{
		MonthFrom: opexMonth(2029, time.June), MonthTo: opexMonth(2029, time.June), Category: "services",
	})
	require.NoError(t, err)
	require.True(t, svc[0].AmountBase.Valid, "rate added → recosted in place (past base not frozen while NULL)")
	require.Equal(t, "100", svc[0].AmountBase.Decimal.String(), "200 TSJ * 0.5")
	_ = contractorID

	// per-month rate (nf08-04): TSD bumps 0.9 → 0.8 from 2029-07. A template active from June,
	// materialised through August, folds June at 0.9 and Jul/Aug at 0.8 — NOT all at today's rate.
	seedRate("TSD", "0.8", 2029, time.July)
	pmID, err := mtr.UpsertOpexRecurring(ctx, entity.OpexRecurringInsert{
		Label: "NF-OPEX-TEST PerMonth", Category: "rentmisc",
		Amount: decimal.RequireFromString("100"), Currency: "TSD", ActiveFrom: opexMonth(2029, time.June),
	}, 0)
	require.NoError(t, err)
	_, err = mtr.MaterializeOpexRecurring(ctx, opexMonth(2029, time.August))
	require.NoError(t, err)
	pm, err := mtr.ListOpexLines(ctx, entity.OpexLineFilter{
		MonthFrom: opexMonth(2029, time.June), MonthTo: opexMonth(2029, time.August), Category: "rentmisc",
	})
	require.NoError(t, err)
	byMonth := map[string]entity.OpexLine{}
	for _, l := range pm {
		byMonth[l.Month.Format("2006-01")] = l
	}
	require.Equal(t, "90", byMonth["2029-06"].AmountBase.Decimal.String(), "June at 0.9")
	require.Equal(t, "80", byMonth["2029-07"].AmountBase.Decimal.String(), "July at 0.8")
	require.Equal(t, "80", byMonth["2029-08"].AmountBase.Decimal.String(), "August at 0.8")
	_ = pmID

	// archiving stops further materialisation: the salary template already booked Jan..Aug while
	// active (the horizon advanced to August across the sub-tests above), but after archiving it must
	// gain NO rows for Sep..Dec even though the horizon advances to December (other active templates
	// still book — assert on the archived template's category and its future months, not a count).
	require.NoError(t, mtr.ArchiveOpexRecurring(ctx, recID))
	_, err = mtr.MaterializeOpexRecurring(ctx, opexMonth(2029, time.December))
	require.NoError(t, err)
	salLater, err := mtr.ListOpexLines(ctx, entity.OpexLineFilter{
		MonthFrom: opexMonth(2029, time.September), MonthTo: opexMonth(2029, time.December), Category: "salaries",
	})
	require.NoError(t, err)
	require.Empty(t, salLater, "archived template materialises nothing for months after it was archived")

	// g25-11: a MATERIALISED line cannot be deleted (it would only resurrect on the next tick and
	// invite a double-count); a manual line still deletes fine.
	jan, err = mtr.ListOpexLines(ctx, entity.OpexLineFilter{
		MonthFrom: opexMonth(2029, time.January), MonthTo: opexMonth(2029, time.January), Category: "salaries",
	})
	require.NoError(t, err)
	require.Len(t, jan, 1)
	require.ErrorIs(t, mtr.DeleteOpexLine(ctx, jan[0].Id), entity.ErrOpexLineMaterialised,
		"deleting a materialised line is refused")
	require.NoError(t, mtr.UpsertOpexLines(ctx, []entity.OpexLineInsert{
		{Month: opexMonth(2029, time.January), Category: "salaries", Label: "NF-OPEX-TEST manual-adj",
			Amount: decimal.RequireFromString("10"), Currency: "EUR", AmountBase: nd("10")},
	}))
	manualLines, err := mtr.ListOpexLines(ctx, entity.OpexLineFilter{
		MonthFrom: opexMonth(2029, time.January), MonthTo: opexMonth(2029, time.January), Category: "salaries",
	})
	require.NoError(t, err)
	for _, l := range manualLines {
		if !l.RecurringId.Valid {
			require.NoError(t, mtr.DeleteOpexLine(ctx, l.Id), "a manual line deletes fine")
		}
	}

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
	// Use a clean 2030 month untouched by the templates above: one costed line (100 EUR) + one
	// uncosted line → total counts 100, caveat set.
	require.NoError(t, mtr.UpsertOpexLines(ctx, []entity.OpexLineInsert{
		{Month: opexMonth(2030, time.August), Category: "other", Label: "NF-OPEX-TEST costed",
			Amount: decimal.RequireFromString("100"), Currency: "EUR", AmountBase: nd("100")},
		{Month: opexMonth(2030, time.August), Category: "other", Label: "NF-OPEX-TEST uncosted",
			Amount: decimal.RequireFromString("50"), Currency: "GBP"},
	}))
	from := opexMonth(2030, time.August)
	to := opexMonth(2030, time.September)
	dash, err := mtr.GetDashboard(ctx, from, to, 10)
	require.NoError(t, err)
	require.Equal(t, "100", dash.OpexTotal.String(), "uncosted line excluded from the total")
	require.NotEmpty(t, dash.OpexCaveat, "uncosted line must flag the operating result")
}

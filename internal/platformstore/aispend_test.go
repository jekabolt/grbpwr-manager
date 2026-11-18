package platformstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

func TestAISpendUpsertAndSum(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	day := func(s string) time.Time { v, _ := time.Parse(time.DateOnly, s); return v }
	d := decimal.RequireFromString

	for _, w := range []struct {
		team int64
		day  string
		v    platformstore.AISpend
	}{
		{2, "2026-10-31", platformstore.AISpend{CostUSD: d("9"), Calls: 9}}, // last month: outside the sum
		{2, "2026-11-01", platformstore.AISpend{CostUSD: d("1.5"), Calls: 3}},
		{2, "2026-11-02", platformstore.AISpend{CostUSD: d("0.1"), Calls: 1}},
		{2, "2026-11-02", platformstore.AISpend{CostUSD: d("2.25"), Unpriced: 1, Calls: 4}}, // the recount replaces the day
		{1, "2026-11-02", platformstore.AISpend{CostUSD: d("0.000001"), Calls: 1}},
	} {
		if err := st.UpsertAISpend(ctx, w.team, day(w.day), w.v); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.AISpendSince(ctx, day("2026-11-01"))
	if err != nil {
		t.Fatal(err)
	}
	if two := got[2]; !two.CostUSD.Equal(d("3.75")) || two.Calls != 7 || two.Unpriced != 1 {
		t.Fatalf("team 2 = %+v", two)
	}
	if one := got[1]; !one.CostUSD.Equal(d("0.000001")) || one.Calls != 1 {
		t.Fatalf("team #1 = %+v", one)
	}
	if _, ok := got[3]; ok || len(got) != 2 {
		t.Fatalf("teams without rows must be absent: %+v", got)
	}
	if err := st.UpsertAISpend(ctx, 0, day("2026-11-02"), platformstore.AISpend{}); err == nil {
		t.Fatal("team 0 accepted")
	}
}

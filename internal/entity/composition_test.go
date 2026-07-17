package entity

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func fp(code, pct string) FiberPercent {
	return FiberPercent{FiberCode: code, Percent: decimal.RequireFromString(pct)}
}

// asMap flattens a derived composition to code -> percent string for order-insensitive assertions.
func asMap(rows []FiberPercent) map[string]string {
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.FiberCode] = r.Percent.String()
	}
	return m
}

func total(rows []FiberPercent) decimal.Decimal {
	t := decimal.Zero
	for _, r := range rows {
		t = t.Add(r.Percent)
	}
	return t
}

func TestDeriveStyleComposition(t *testing.T) {
	cases := []struct {
		name    string
		fabrics [][]FiberPercent
		want    map[string]string
	}{
		{"single fabric keeps its composition", [][]FiberPercent{{fp("COT", "60"), fp("POL", "40")}},
			map[string]string{"COT": "60", "POL": "40"}},
		{"single pure fabric", [][]FiberPercent{{fp("COT", "100")}}, map[string]string{"COT": "100"}},
		{"two pure fabrics divide equally", [][]FiberPercent{{fp("COT", "100")}, {fp("POL", "100")}},
			map[string]string{"COT": "50", "POL": "50"}},
		{"mixed + pure fabric", [][]FiberPercent{{fp("COT", "60"), fp("POL", "40")}, {fp("WOL", "100")}},
			map[string]string{"COT": "30", "POL": "20", "WOL": "50"}},
		{"duplicate fibre across fabrics sums", [][]FiberPercent{{fp("COT", "100")}, {fp("COT", "100")}},
			map[string]string{"COT": "100"}},
		{"fibre code normalised to upper", [][]FiberPercent{{fp("cot", "100")}}, map[string]string{"COT": "100"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DeriveStyleComposition(tc.fabrics)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			gm := asMap(got)
			if len(gm) != len(tc.want) {
				t.Fatalf("got %v, want %v", gm, tc.want)
			}
			for code, want := range tc.want {
				if gm[code] != want {
					t.Errorf("fibre %s = %s, want %s (full: %v)", code, gm[code], want, gm)
				}
			}
			if !total(got).Equal(decimal.NewFromInt(100)) {
				t.Errorf("total = %s, want exactly 100", total(got).String())
			}
		})
	}
}

func TestDeriveStyleComposition_RoundingTotalsExactly100(t *testing.T) {
	// Three pure fabrics -> 33.33 each -> total 99.99; the residual is absorbed so the total is 100.
	got, err := DeriveStyleComposition([][]FiberPercent{{fp("COT", "100")}, {fp("POL", "100")}, {fp("WOL", "100")}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !total(got).Equal(decimal.NewFromInt(100)) {
		t.Fatalf("total = %s, want exactly 100", total(got).String())
	}
	if len(got) != 3 {
		t.Fatalf("want 3 fibres, got %d", len(got))
	}
}

func TestDeriveStyleComposition_Empty(t *testing.T) {
	got, err := DeriveStyleComposition(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("empty input should yield nil, got %v", got)
	}
}

func TestDeriveStyleComposition_UnbalancedFabricRejected(t *testing.T) {
	// A fabric that does not itself sum to 100 must be rejected, not silently nudged.
	_, err := DeriveStyleComposition([][]FiberPercent{{fp("COT", "50")}})
	if err == nil {
		t.Fatal("expected a field-tagged error for a fabric that totals 50")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "composition" {
		t.Fatalf("expected a composition field violation, got %v", err)
	}
}

func TestReconcileStyleComposition_ManualNeverOverwritten(t *testing.T) {
	manual := []FiberPercent{fp("COT", "100")}
	derived := []FiberPercent{fp("POL", "50"), fp("COT", "50")}

	src, rows := ReconcileStyleComposition(CompositionSourceManual, manual, derived)
	if src != CompositionSourceManual {
		t.Fatalf("source = %s, want manual", src)
	}
	if len(rows) != 1 || rows[0].FiberCode != "COT" {
		t.Fatalf("manual override must be preserved, got %v", asMap(rows))
	}

	src, rows = ReconcileStyleComposition(CompositionSourceAuto, nil, derived)
	if src != CompositionSourceAuto || len(rows) != 2 {
		t.Fatalf("auto should take the derived set, got source=%s rows=%v", src, asMap(rows))
	}
}

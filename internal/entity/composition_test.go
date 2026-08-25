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

// TestCountsTowardStyleComposition pins the назначение filter that section alone cannot express:
// карманка / подкладка / бортовка / утеплитель are all legitimately section='fabric' roll goods, and
// none of them is what the garment is MADE OF. Regression for SS26-008, which read
// hemp 50 / viscose 45 / polyester 5 for a 100% hemp shell because its pocketing line took an equal
// 1/N share.
func TestCountsTowardStyleComposition(t *testing.T) {
	counts := []TechCardBomPurpose{
		BomPurposeMain,
		BomPurposeContrast,
		BomPurposeMesh,
		BomPurposeOther, // role lives in a free-text note — undecidable, so it keeps contributing
		"",              // NULL column: "не разобрано", the state most lines on file are in today
	}
	for _, p := range counts {
		if !CountsTowardStyleComposition(p) {
			t.Errorf("purpose %q must count toward the style composition", p)
		}
	}

	excluded := []TechCardBomPurpose{
		BomPurposePocketing,
		BomPurposeLining,
		BomPurposeInterfacing,
		BomPurposeInsulation,
	}
	for _, p := range excluded {
		if CountsTowardStyleComposition(p) {
			t.Errorf("purpose %q must NOT count toward the style composition", p)
		}
	}
}

// TestCountsTowardStyleCompositionCoversVocabulary guards the deny-list against vocabulary drift: a
// purpose added to BomPurposeOrder without a decision here silently starts contributing. That is the
// SAFE default (nothing empties), so this test does not fail on the new value — it fails only if the
// deny-list names something that is no longer a purpose at all, which would be a dead entry.
func TestCountsTowardStyleCompositionCoversVocabulary(t *testing.T) {
	for p := range styleCompositionExcludedPurposes {
		if !IsValidTechCardBomPurpose(p) {
			t.Errorf("deny-list names %q, which is not a valid BOM purpose any more", p)
		}
	}
}

// TestDeriveStyleCompositionSS26008 reproduces the reported card end to end at the entity layer: the
// 100% hemp shell alone, once the карманка line is filtered out, derives 100% hemp — not the
// hemp 50 / viscose 45 / polyester 5 that the equal split produced when both lines went in.
func TestDeriveStyleCompositionSS26008(t *testing.T) {
	shell := []FiberPercent{fp("HMP", "100")}
	pocketing := []FiberPercent{fp("VIS", "90"), fp("POL", "10")}

	before, err := DeriveStyleComposition([][]FiberPercent{shell, pocketing})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := asMap(before); got["HMP"] != "50" {
		t.Fatalf("precondition: unfiltered derive should still split equally, got %v", got)
	}

	var fabrics [][]FiberPercent
	for _, line := range []struct {
		purpose TechCardBomPurpose
		fibres  []FiberPercent
	}{
		{BomPurposeMain, shell},
		{BomPurposePocketing, pocketing},
	} {
		if CountsTowardStyleComposition(line.purpose) {
			fabrics = append(fabrics, line.fibres)
		}
	}
	after, err := DeriveStyleComposition(fabrics)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := asMap(after)
	if len(got) != 1 || got["HMP"] != "100" {
		t.Fatalf("filtered derive = %v, want HMP 100 only", got)
	}
	if !total(after).Equal(decimal.NewFromInt(100)) {
		t.Fatalf("total = %s, want 100", total(after))
	}
}

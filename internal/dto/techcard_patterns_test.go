package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// ndFrom builds a valid NullDecimal from a string for tests.
func ndFrom(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
}

func decEq(t *testing.T, nd decimal.NullDecimal, want string) {
	t.Helper()
	if !nd.Valid || !nd.Decimal.Equal(decimal.RequireFromString(want)) {
		t.Fatalf("got %v (valid=%v), want %s", nd.Decimal, nd.Valid, want)
	}
}

// TestColorwayUsageTotals locks the per-usage cost formulas: a measured usage is
// consumption × price grossed up by the article wastage; a countable trim is quantity ×
// price with NO wastage; a per-size usage moves its cost to size_run_total (line_total
// goes invalid) and folds per-size consumption against order quantities.
func TestColorwayUsageTotals(t *testing.T) {
	bom := &entity.TechCardBomItem{UnitPrice: ndFrom("10"), WastagePercent: ndFrom("10")}
	bomNoWaste := &entity.TechCardBomItem{UnitPrice: ndFrom("10")}

	// measured consumption, no per-size: line_total = 2 × 10 × 1.1 = 22; size_run invalid.
	measured := entity.TechCardColorwayUsage{Consumption: ndFrom("2")}
	decEq(t, measured.LineTotal(bom), "22")
	if rt := measured.SizeRunTotal(bom, map[int]int{4: 10}); rt.Valid {
		t.Fatalf("size_run_total should be invalid without per-size consumption")
	}

	// countable quantity: 3 × 10 = 30, wastage N/A even though the article has a wastage %.
	countable := entity.TechCardColorwayUsage{Quantity: ndFrom("3")}
	decEq(t, countable.LineTotal(bom), "30")
	decEq(t, countable.LineTotal(bomNoWaste), "30")

	// per-size: line_total invalid; size_run = (1.5×10 + 1.8×20) × 10 × 1.1 = 51 × 11 = 561.
	perSize := entity.TechCardColorwayUsage{
		Consumption: ndFrom("2"), // ignored once size consumptions are present
		SizeConsumptions: []entity.TechCardBomSizeConsumption{
			{SizeId: 4, Consumption: decimal.RequireFromString("1.5")},
			{SizeId: 5, Consumption: decimal.RequireFromString("1.8")},
		},
	}
	if lt := perSize.LineTotal(bom); lt.Valid {
		t.Fatalf("line_total must be invalid when per-size consumption is present")
	}
	decEq(t, perSize.SizeRunTotal(bom, map[int]int{4: 10, 5: 20}), "561")
	// a size with no order quantity contributes nothing: only size 4 → 1.5×10 × 10 × 1.1 = 165.
	decEq(t, perSize.SizeRunTotal(bom, map[int]int{4: 10}), "165")
	// no order quantities at all → invalid (cost is 0 until a size run is set).
	if rt := perSize.SizeRunTotal(bom, map[int]int{}); rt.Valid {
		t.Fatalf("size_run_total with no quantities should be invalid")
	}

	// EffectiveTotal prefers size_run when valid, else falls back to line_total.
	decEq(t, perSize.EffectiveTotal(bom, map[int]int{4: 10, 5: 20}), "561")
	decEq(t, measured.EffectiveTotal(bom, map[int]int{4: 10}), "22")

	// no article (unresolved index) → no cost.
	if lt := measured.LineTotal(nil); lt.Valid {
		t.Fatalf("line_total against a nil article should be invalid")
	}
}

// TestColorwayCostRollup checks one colourway's per-currency rollup: usages bucket by their
// article currency, currency-less lines fold into the costing currency, a foreign currency
// is excluded and flags has_unconverted_currencies.
func TestColorwayCostRollup(t *testing.T) {
	eur := sql.NullString{String: "EUR", Valid: true}
	usd := sql.NullString{String: "USD", Valid: true}
	bomItems := []entity.TechCardBomItem{
		{UnitPrice: ndFrom("10"), Currency: eur}, // index 0
		{UnitPrice: ndFrom("3"), Currency: usd},  // index 1
		{UnitPrice: ndFrom("5")},                 // index 2, currency-less
	}
	idx := func(i int32) sql.NullInt32 { return sql.NullInt32{Int32: i, Valid: true} }
	cw := entity.TechCardColorway{Usages: []entity.TechCardColorwayUsage{
		{BomItemIndex: idx(0), Quantity: ndFrom("2")}, // 20 EUR
		{BomItemIndex: idx(1), Quantity: ndFrom("1")}, // 3 USD
		{BomItemIndex: idx(2), Quantity: ndFrom("4")}, // 20 currency-less
	}}
	res := colorwayCost(&cw, bomItems, "EUR", map[int]int{}, 0)
	// materials_per_unit = EUR(20) + currency-less(20) = 40; USD excluded. All usages are
	// per-garment (countable Quantity), so totalOrderQty is irrelevant here.
	if !res.materialsPerUnit.Equal(decimal.RequireFromString("40")) {
		t.Fatalf("materials_per_unit = %v, want 40", res.materialsPerUnit)
	}
	if !res.hasUnconverted {
		t.Fatalf("expected has_unconverted_currencies (USD usage vs EUR costing)")
	}
	byCcy := map[string]string{}
	for _, l := range res.materialsTotal {
		byCcy[l.Currency] = l.Amount.Value
	}
	if byCcy["EUR"] != "20" || byCcy["USD"] != "3" || byCcy[""] != "20" {
		t.Fatalf("materials_total buckets mismatch: %+v", byCcy)
	}
}

// TestTechCardPatternsRoundTrip covers parsing and re-emitting the per-size PDF выкройки.
func TestTechCardPatternsRoundTrip(t *testing.T) {
	in := &pb_common.TechCardInsert{
		StyleNumber: "ST-PAT",
		Name:        "Coat",
		SizeIds:     []int32{4, 5},
		Patterns: []*pb_common.TechCardSizePattern{
			{SizeId: 4, Url: "https://cdn/x4.pdf", Filename: "front-m.pdf", SizeBytes: 1234},
			{SizeId: 4, Url: "https://cdn/x4b.pdf"}, // multiple per size is allowed
			{SizeId: 5, Url: "https://cdn/x5.pdf"},
		},
	}
	ent, err := ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if len(ent.Patterns) != 3 || ent.Patterns[0].SizeId != 4 || ent.Patterns[0].URL != "https://cdn/x4.pdf" {
		t.Fatalf("patterns not parsed: %+v", ent.Patterns)
	}
	out := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *ent})
	if len(out.TechCard.Patterns) != 3 || out.TechCard.Patterns[0].Filename != "front-m.pdf" || out.TechCard.Patterns[0].SizeBytes != 1234 {
		t.Fatalf("patterns round-trip mismatch: %+v", out.TechCard.Patterns)
	}
}

func TestTechCardPatternAndUsageValidation(t *testing.T) {
	cases := map[string]*pb_common.TechCardInsert{
		"pattern size not in range": {StyleNumber: "x", Name: "y", SizeIds: []int32{4},
			Patterns: []*pb_common.TechCardSizePattern{{SizeId: 9, Url: "u"}}},
		"pattern url required": {StyleNumber: "x", Name: "y", SizeIds: []int32{4},
			Patterns: []*pb_common.TechCardSizePattern{{SizeId: 4, Url: "  "}}},
		"pattern url not http": {StyleNumber: "x", Name: "y", SizeIds: []int32{4},
			Patterns: []*pb_common.TechCardSizePattern{{SizeId: 4, Url: "javascript:alert(1)"}}},
		"usage size_consumption not in range": {StyleNumber: "x", Name: "y", SizeIds: []int32{4},
			Colorways: []*pb_common.TechCardColorway{{Name: "Black", Usages: []*pb_common.TechCardColorwayUsage{{
				SizeConsumptions: []*pb_common.TechCardBomSizeConsumption{{SizeId: 9, Consumption: &pb_decimal.Decimal{Value: "1"}}},
			}}}}},
		"usage size_consumption negative": {StyleNumber: "x", Name: "y", SizeIds: []int32{4},
			Colorways: []*pb_common.TechCardColorway{{Name: "Black", Usages: []*pb_common.TechCardColorwayUsage{{
				SizeConsumptions: []*pb_common.TechCardBomSizeConsumption{{SizeId: 4, Consumption: &pb_decimal.Decimal{Value: "-1"}}},
			}}}}},
	}
	for name, in := range cases {
		if _, err := ConvertPbTechCardInsertToEntity(in); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

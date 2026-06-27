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

func TestTechCardBomSizeRunTotal(t *testing.T) {
	b := entity.TechCardBomItem{
		UnitPrice: ndFrom("2.00"),
		SizeConsumptions: []entity.TechCardBomSizeConsumption{
			{SizeId: 4, Consumption: decimal.RequireFromString("1.5")},
			{SizeId: 5, Consumption: decimal.RequireFromString("1.8")},
		},
	}
	// (1.5*10 + 1.8*20) * 2.00 = 51 * 2 = 102
	got := b.SizeRunTotal(map[int]int{4: 10, 5: 20})
	if !got.Valid || !got.Decimal.Equal(decimal.RequireFromString("102")) {
		t.Fatalf("run total = %v (valid=%v), want 102", got.Decimal, got.Valid)
	}
	// with 10% wastage: 102 * 1.1 = 112.2
	b.WastagePercent = ndFrom("10")
	got = b.SizeRunTotal(map[int]int{4: 10, 5: 20})
	if !got.Valid || !got.Decimal.Equal(decimal.RequireFromString("112.2")) {
		t.Fatalf("run total w/ wastage = %v, want 112.2", got.Decimal)
	}
	// a size with no order quantity contributes nothing; here only size 4 has qty.
	got = b.SizeRunTotal(map[int]int{4: 10})
	if !got.Valid || !got.Decimal.Equal(decimal.RequireFromString("33")) { // 1.5*10*2*1.1
		t.Fatalf("run total (partial qty) = %v, want 33", got.Decimal)
	}
	// no order quantities at all -> invalid so the rollup falls back to LineTotal.
	if got := b.SizeRunTotal(map[int]int{}); got.Valid {
		t.Fatalf("run total with no quantities should be invalid, got %v", got.Decimal)
	}
	// no per-size consumption -> invalid.
	empty := entity.TechCardBomItem{UnitPrice: ndFrom("2")}
	if got := empty.SizeRunTotal(map[int]int{4: 10}); got.Valid {
		t.Fatalf("run total with no size consumption should be invalid")
	}
}

func TestEffectiveBomLineTotalFallback(t *testing.T) {
	// no per-size -> per-garment LineTotal (consumption*price).
	perGarment := entity.TechCardBomItem{Consumption: ndFrom("2"), UnitPrice: ndFrom("3")}
	if eff := effectiveBomLineTotal(&perGarment, map[int]int{4: 10}); !eff.Valid || !eff.Decimal.Equal(decimal.RequireFromString("6")) {
		t.Fatalf("effective (fallback) = %v, want 6", eff.Decimal)
	}
	// per-size present + quantities -> size-run total.
	perSize := entity.TechCardBomItem{
		UnitPrice:        ndFrom("2"),
		SizeConsumptions: []entity.TechCardBomSizeConsumption{{SizeId: 4, Consumption: decimal.RequireFromString("1.5")}},
	}
	if eff := effectiveBomLineTotal(&perSize, map[int]int{4: 10}); !eff.Valid || !eff.Decimal.Equal(decimal.RequireFromString("30")) {
		t.Fatalf("effective (run) = %v, want 30", eff.Decimal)
	}
}

func TestBomMaterialsRollupUsesRunTotal(t *testing.T) {
	eur := sql.NullString{String: "EUR", Valid: true}
	items := []entity.TechCardBomItem{
		{ // per-size line: 1.5*10*2 = 30
			Currency:         eur,
			UnitPrice:        ndFrom("2"),
			SizeConsumptions: []entity.TechCardBomSizeConsumption{{SizeId: 4, Consumption: decimal.RequireFromString("1.5")}},
		},
		{Currency: eur, Consumption: ndFrom("2"), UnitPrice: ndFrom("3")}, // per-garment: 6
	}
	lines, cost := bomMaterialsRollup(items, eur, map[int]int{4: 10})
	if !cost.Equal(decimal.RequireFromString("36")) { // 30 + 6
		t.Fatalf("materials cost = %v, want 36", cost)
	}
	if len(lines) != 1 || lines[0].Currency != "EUR" {
		t.Fatalf("unexpected rollup lines: %+v", lines)
	}
}

func TestTechCardPatternsAndColorwayMaterialsRoundTrip(t *testing.T) {
	in := &pb_common.TechCardInsert{
		StyleNumber: "ST-PAT",
		Name:        "Coat",
		SizeIds:     []int32{4, 5},
		Colorways: []*pb_common.TechCardColorway{
			{Name: "Black"},
			{Name: "Navy"},
		},
		BomItems: []*pb_common.TechCardBomItem{
			{
				Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
				Name:    "Main fabric",
				ColorwayColors: []*pb_common.TechCardBomColorwayColor{
					{ColorwayIndex: 0, Color: "Jet"},
					{ColorwayIndex: 1, Color: "Indigo"},
				},
				SizeConsumptions: []*pb_common.TechCardBomSizeConsumption{
					{SizeId: 4, Consumption: &pb_decimal.Decimal{Value: "1.5"}},
					{SizeId: 5, Consumption: &pb_decimal.Decimal{Value: "1.8"}},
				},
			},
		},
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
	if len(ent.BomItems[0].SizeConsumptions) != 2 {
		t.Fatalf("size consumptions not parsed: %+v", ent.BomItems[0].SizeConsumptions)
	}

	out := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *ent})
	if len(out.TechCard.Patterns) != 3 || out.TechCard.Patterns[0].Filename != "front-m.pdf" || out.TechCard.Patterns[0].SizeBytes != 1234 {
		t.Fatalf("patterns round-trip mismatch: %+v", out.TechCard.Patterns)
	}
	if len(out.TechCard.Colorways) != 2 {
		t.Fatalf("colorways: %d", len(out.TechCard.Colorways))
	}
	black := out.TechCard.Colorways[0]
	if len(black.Materials) != 1 || black.Materials[0].MaterialName != "Main fabric" ||
		black.Materials[0].Color != "Jet" || black.Materials[0].BomItemIndex != 0 {
		t.Fatalf("colorway[0] materials mismatch: %+v", black.Materials)
	}
	navy := out.TechCard.Colorways[1]
	if len(navy.Materials) != 1 || navy.Materials[0].Color != "Indigo" {
		t.Fatalf("colorway[1] materials mismatch: %+v", navy.Materials)
	}
	// size-run total surfaced on the BOM line: (1.5*10 + 1.8*20)*... but no quantities
	// here, so it must be empty and line_total carries the per-garment value (none set).
	if out.TechCard.BomItems[0].SizeRunTotal != nil {
		t.Fatalf("size_run_total should be empty without order quantities: %v", out.TechCard.BomItems[0].SizeRunTotal)
	}
}

func TestTechCardPatternAndConsumptionValidation(t *testing.T) {
	cases := map[string]*pb_common.TechCardInsert{
		"pattern size not in range": {StyleNumber: "x", Name: "y", SizeIds: []int32{4},
			Patterns: []*pb_common.TechCardSizePattern{{SizeId: 9, Url: "u"}}},
		"pattern url required": {StyleNumber: "x", Name: "y", SizeIds: []int32{4},
			Patterns: []*pb_common.TechCardSizePattern{{SizeId: 4, Url: "  "}}},
		"pattern url not http": {StyleNumber: "x", Name: "y", SizeIds: []int32{4},
			Patterns: []*pb_common.TechCardSizePattern{{SizeId: 4, Url: "javascript:alert(1)"}}},
		"bom consumption size not in range": {StyleNumber: "x", Name: "y", SizeIds: []int32{4},
			BomItems: []*pb_common.TechCardBomItem{{
				Section:          pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
				Name:             "f",
				SizeConsumptions: []*pb_common.TechCardBomSizeConsumption{{SizeId: 9, Consumption: &pb_decimal.Decimal{Value: "1"}}},
			}}},
		"bom consumption negative": {StyleNumber: "x", Name: "y", SizeIds: []int32{4},
			BomItems: []*pb_common.TechCardBomItem{{
				Section:          pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
				Name:             "f",
				SizeConsumptions: []*pb_common.TechCardBomSizeConsumption{{SizeId: 4, Consumption: &pb_decimal.Decimal{Value: "-1"}}},
			}}},
	}
	for name, in := range cases {
		if _, err := ConvertPbTechCardInsertToEntity(in); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

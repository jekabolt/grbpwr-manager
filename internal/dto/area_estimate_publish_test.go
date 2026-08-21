package dto

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

// ПУБЛИКАЦИЯ ОЦЕНКИ РАСХОДА (Ф1, видимая половина). Рецепт колорвея рисуется одним списком по
// тканям; у слота, посчитанного оценкой, строки рецепта нет вовсе, поэтому расход в этой строке
// может назвать только сервер. Тесты ниже стерегут ровно то, чем это опасно: чтобы напечатанное
// число было ТЕМ ЖЕ, которым посчитаны деньги, и чтобы отсутствие числа называло причину.

// measuredCard extends estimateCard with a real measurement provenance and a two-size range, so the
// published figure is a RANGE AVERAGE (the style basis) and not one size's norm.
func measuredCard() *entity.TechCard {
	tc := estimateCard()
	tc.SizeIds = []int{4, 5}
	tc.PieceAreaScopes = map[string]entity.PieceAreaScope{
		"BOMKEY1": {ScopeKey: "BOMKEY1", Rows: []entity.PieceAreaRow{
			{
				PieceLineKey:    "PIECE1",
				SizeId:          sql.NullInt64{Int64: 4, Valid: true},
				AreaCm2:         decimal.RequireFromString("14000"),
				ContourLayer:    "1",
				SeamAllowanceMm: decimal.RequireFromString("10"),
				ParsedBy:        "kate",
				ParsedAt:        time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC),
			},
			{
				PieceLineKey:    "PIECE1",
				SizeId:          sql.NullInt64{Int64: 5, Valid: true},
				AreaCm2:         decimal.RequireFromString("21000"),
				ContourLayer:    "1",
				SeamAllowanceMm: decimal.RequireFromString("10"),
				ParsedBy:        "kate",
				ParsedAt:        time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC),
			},
		}},
	}
	return tc
}

func publishedEstimates(t *testing.T, tc *entity.TechCard) []*pb_common.TechCardSlotAreaEstimate {
	t.Helper()
	refs := techCardColorwayRefsToPb(tc, nil, CostingFx{})
	if len(refs) == 0 {
		t.Fatal("card read published no colourways at all")
	}
	return refs[0].GetAreaEstimates()
}

// pinnedFx is the FX of the cross-projection tests: EUR base, no rates needed because every figure
// is already in EUR once the pin's price is resolved.
var pinnedFx = CostingFx{Base: "EUR"}

// pinnedMeasuredCard is the fixture the cross-projection tests need, and every element of it is
// load-bearing — a card without them agrees by accident:
//
//   - a PIN, so the answer depends on LinkedMaterials and not on the BOM snapshot alone;
//   - a SELVEDGE, so the pinned article's CUTTING width (150 − 2×5 = 140) differs from the slot's
//     snapshot width (150) and a projection reading the wrong one produces a different metreage;
//   - TWO currencies on the pinned article and NEITHER of them the costing currency, so the price
//     resolves only through the BASE currency and a projection passing an empty base finds no price
//     at all (LatestPriceForCurrencies returns nil on two unmatched currencies).
//
// Numbers: sizes 4 and 5 measure 14 000 and 21 000 cm² → 100 cm and 150 cm on a 140 cm cutting
// width → the range average is 1.25 m, at 50 EUR/m = 62.50 EUR. On the WRONG (snapshot) width the
// same card reads 1.1667 m / 58.33 EUR — a plausible number, which is the point.
func pinnedMeasuredCard() *entity.TechCard {
	tc := measuredCard()
	tc.Costing = &entity.TechCardCosting{Currency: sql.NullString{String: "USD", Valid: true}}
	tc.BomItems[0].MaterialId = sql.NullInt64{Int64: 800, Valid: true}
	// The slot's SNAPSHOT width is the full roll — deliberately NOT the cutting width, so a reader
	// that falls back to it divides by 150 instead of 140 and lands on a different, plausible
	// metreage. Without this the fixture agrees with itself no matter which width is read.
	tc.BomItems[0].FabricWidth = decimal.NullDecimal{Decimal: decimal.RequireFromString("150"), Valid: true}

	slot := entity.MaterialWithPrice{}
	slot.Id = 800
	slot.FabricAttr = &entity.MaterialFabricAttr{
		WidthCm: decimal.NullDecimal{Decimal: decimal.RequireFromString("150"), Valid: true},
	}
	slot.Unit = sql.NullString{String: "m", Valid: true}

	pinned := entity.MaterialWithPrice{}
	pinned.Id = 900
	pinned.Unit = sql.NullString{String: "m", Valid: true}
	pinned.FabricAttr = &entity.MaterialFabricAttr{
		WidthCm:    decimal.NullDecimal{Decimal: decimal.RequireFromString("150"), Valid: true},
		SelvedgeCm: decimal.RequireFromString("5"),
	}
	pinned.LatestPrices = map[string]*entity.MaterialPrice{
		"EUR": {MaterialId: 900, Price: decimal.RequireFromString("50"), Currency: "EUR"},
		"GBP": {MaterialId: 900, Price: decimal.RequireFromString("44"), Currency: "GBP"},
	}
	tc.LinkedMaterials = map[int]entity.MaterialWithPrice{800: slot, 900: pinned}

	tc.Colorways[0].Usages = []entity.TechCardColorwayUsage{{
		Id:         34,
		BomItemId:  sql.NullInt64{Int64: 56, Valid: true},
		PieceId:    sql.NullInt64{Int64: 14, Valid: true},
		MaterialId: sql.NullInt64{Int64: 900, Valid: true},
	}}
	return tc
}

// TestThreeProjectionsAgreeOnOneSlot is the phase's promise stated where it can actually fail: on
// the three PUBLISHED artefacts, not on an internal slice they happen to share.
//
// The recipe row (card read), the costing headline (same read) and the смета (GetStyleCostEstimate)
// are three screens describing one fabric of one colourway. Before this test they were three
// argument assemblies around one function, and one of them — the смета — lent the estimate a price
// catalogue with no fabric attributes, so it silently divided by the roll width instead of the
// cutting width and printed 58.33 EUR beside the other two printing 62.50.
func TestThreeProjectionsAgreeOnOneSlot(t *testing.T) {
	tc := pinnedMeasuredCard()

	// 1. The recipe row: consumption per garment.
	full := ConvertEntityTechCardToPb(tc, pinnedFx)
	if len(full.GetColorways()) != 1 || len(full.GetColorways()[0].GetAreaEstimates()) != 1 {
		t.Fatalf("card read published %d colourways / %d estimates, want 1 and 1",
			len(full.GetColorways()), len(full.GetColorways()[0].GetAreaEstimates()))
	}
	est := full.GetColorways()[0].GetAreaEstimates()[0]
	perGarment, err := nullDecimalFromPb(est.GetPerGarment())
	if err != nil || !perGarment.Valid {
		t.Fatalf("recipe row published no consumption (%v); refusal=%q", err, est.GetRefusal())
	}
	if !perGarment.Decimal.Equal(decimal.RequireFromString("1.25")) {
		t.Fatalf("per_garment = %s, want 1.25 — 1.1666… means the CUTTING width lost its selvedge", perGarment.Decimal)
	}

	// 2. The costing headline: the same slot's money, in the pinned article's currency.
	var costing decimal.Decimal
	var found bool
	for _, l := range full.GetTechCard().GetCosting().GetColorwayCosts()[0].GetMaterialsTotal() {
		if l.GetCurrency() == "EUR" {
			v, _ := nullDecimalFromPb(l.GetAmount())
			costing, found = v.Decimal, true
		}
	}
	if !found {
		t.Fatal("the costing headline carries no EUR line; the pinned article's price never resolved through the base currency")
	}
	want := perGarment.Decimal.Mul(decimal.RequireFromString("50"))
	if !costing.Equal(want) {
		t.Fatalf("costing charged %s EUR, the recipe printed %s m × 50 = %s EUR", costing, perGarment.Decimal, want)
	}

	// 3. The смета: the same slot again, folded to base (already EUR).
	sm := ComputeStyleCostEstimate(tc, 35, nil, pinnedFx)
	var smetaTotal decimal.Decimal
	var smetaFound bool
	for _, l := range sm.GetMaterials() {
		if l.GetBomItemId() == 56 {
			v, _ := nullDecimalFromPb(l.GetLineTotalBase())
			smetaTotal, smetaFound = v.Decimal, true
		}
	}
	if !smetaFound {
		t.Fatal("the смета does not list the estimated slot at all")
	}
	if !smetaTotal.Equal(roundMoney(costing)) {
		t.Fatalf("смета %s vs costing %s — two screens, one fabric, two numbers", smetaTotal, roundMoney(costing))
	}
}

// TestSelvedgeIsNotDroppedByTheStyleEstimate isolates the discriminator of the test above, so a
// regression names its own cause instead of failing a three-way comparison.
//
// The селвedge is 6.7% of this roll. A projection that divides by the full width understates the
// metreage — and therefore the purchase and the cost — by exactly that, on every fabric in the
// catalogue that has a кромка entered.
func TestSelvedgeIsNotDroppedByTheStyleEstimate(t *testing.T) {
	tc := pinnedMeasuredCard()
	sm := ComputeStyleCostEstimate(tc, 35, nil, pinnedFx)
	for _, l := range sm.GetMaterials() {
		if l.GetBomItemId() != 56 {
			continue
		}
		total, _ := nullDecimalFromPb(l.GetLineTotalBase())
		if !total.Decimal.Equal(decimal.RequireFromString("62.50")) {
			t.Fatalf("смета line = %s, want 62.50 (1.25 m × 50); 58.33 is the roll width used as the cutting width", total.Decimal)
		}
		return
	}
	t.Fatal("the смета does not list the estimated slot")
}

// TestPlanReasonSurvivesACurrencyItCannotPrice.
//
// The material plan asks ONE question — «is there an area estimate for this slot?» — to choose
// between «нормы расхода нет» (go type a norm) and «есть только оценка по площади» (go measure a
// marker). It passes no base currency, so while the answer went through the MONEY the pinned
// article's price had to resolve in the costing currency alone: a card costed in USD whose article
// is priced in EUR and GBP produced «нормы нет» about a slot whose geometry is fully computed, and
// sent the operator to type a number instead of to cut a marker. The question is now geometric, so
// the currency cannot reach it.
func TestPlanReasonSurvivesACurrencyItCannotPrice(t *testing.T) {
	tc := pinnedMeasuredCard()
	b := &tc.BomItems[0]
	cw := &tc.Colorways[0]

	// Exactly the call the plan makes: no base currency at all.
	if got := slotAreaEstimate(tc, cw, b, tc.LinkedMaterials, tc.CostingBasis(), ""); !got.normDerived {
		t.Fatalf("no norm derived without a base currency (refusal=%q); the plan would say «нормы расхода нет» about measured patterns", got.refusal)
	}
	// And the money really is unavailable there — i.e. the test is not passing because the currency
	// happened to resolve anyway.
	if got := slotAreaEstimate(tc, cw, b, tc.LinkedMaterials, tc.CostingBasis(), ""); got.ok {
		t.Fatal("the pinned article priced itself without a base currency; the fixture no longer covers the case")
	}
}

// TestPublishedNormIsTheNumberTheMoneyUsed is the whole point of the phase, as one assertion.
//
// If it fails, the recipe row prints one consumption while the costing headline charged for
// another. Both look plausible, neither says so, and the disagreement is invisible until somebody
// multiplies the printed metres by the printed price and gets a third number.
//
// It compares against colorwayCost's OWN slice — the money path's working, not a re-derivation —
// so the only way to pass is to keep publishing the figure the money was built from.
func TestPublishedNormIsTheNumberTheMoneyUsed(t *testing.T) {
	tc := measuredCard()
	cc := colorwayCost(tc, &tc.Colorways[0], tc.BomItems, tc.LinkedMaterials, "EUR", tc.CostingBasis(), CostingFx{})

	var used *slotEstimate
	for i := range cc.estimates {
		if cc.estimates[i].ok {
			used = &cc.estimates[i]
		}
	}
	if used == nil {
		t.Fatal("the money path costed no slot by estimate; the fixture no longer covers the case")
	}

	published := publishedEstimates(t, tc)
	if len(published) != 1 {
		t.Fatalf("published %d estimates, want exactly the one slot the money used", len(published))
	}
	got, err := nullDecimalFromPb(published[0].GetPerGarment())
	if err != nil || !got.Valid {
		t.Fatalf("published per_garment is unreadable (%v); the row would print nothing", err)
	}
	if !got.Decimal.Equal(used.perGarment) {
		t.Fatalf("published %s, money used %s — recipe and cost disagree about one fabric", got.Decimal, used.perGarment)
	}
	// And the money really is that norm × the price, so the three numbers on screen close.
	if !used.money.Equal(used.perGarment.Mul(decimal.RequireFromString("100"))) {
		t.Fatalf("money %s ≠ per_garment %s × 100 EUR/m", used.money, used.perGarment)
	}
}

// TestPublishedNormUsesTheRangeAverageNotOneSize pins the BASIS.
//
// The style cost is the simple mean over the declared size range (T6). Publishing the base size's
// norm instead would put the recipe and the cost apart by the whole grading spread — here 1 m vs
// 1.25 m, i.e. 25% of the fabric, on a card where every screen claims to describe one garment.
func TestPublishedNormUsesTheRangeAverageNotOneSize(t *testing.T) {
	tc := measuredCard()
	published := publishedEstimates(t, tc)
	if len(published) != 1 {
		t.Fatalf("published %d estimates, want 1", len(published))
	}
	got, _ := nullDecimalFromPb(published[0].GetPerGarment())
	// (14000 + 21000) cm² / 2 sizes / 140 cm = 125 cm = 1.25 m.
	if !got.Decimal.Equal(decimal.RequireFromString("1.25")) {
		t.Fatalf("per_garment = %s, want 1.25 (range average); a single size's norm would read 1", got.Decimal)
	}
	if published[0].GetUnit() != "m" {
		t.Fatalf("unit = %q, want the SLOT's unit %q — a number without its unit is off by 100 as easily as by 1", published[0].GetUnit(), "m")
	}
}

// TestAuthoredRowSuppressesThePublishedEstimate: a slot whose recipe carries a per-garment row must
// publish NO estimate. Otherwise the client's one-list-by-fabric shows the same fabric twice — once
// with the number a human typed, once with a derived one that is lower by every inter-piece waste —
// and has no rule for which of them is the consumption.
func TestAuthoredRowSuppressesThePublishedEstimate(t *testing.T) {
	tc := measuredCard()
	tc.Colorways[0].Usages = append(tc.Colorways[0].Usages, entity.TechCardColorwayUsage{
		Id:          40,
		BomItemId:   sql.NullInt64{Int64: 56, Valid: true},
		Consumption: decimal.NullDecimal{Decimal: decimal.RequireFromString("2"), Valid: true},
	})
	if got := publishedEstimates(t, tc); len(got) != 0 {
		t.Fatalf("published %d estimates for a slot that has an authored norm; the fabric would be listed twice", len(got))
	}
}

// TestStaleAreasArePublishedAsStaleNotDropped.
//
// Patterns that moved after the measurement describe files that no longer exist. Dropping the row
// silently would leave the operator staring at a fabric with no consumption and no reason — and the
// reason is the one thing that tells them to re-measure rather than to type a norm by hand.
func TestStaleAreasArePublishedAsStaleNotDropped(t *testing.T) {
	tc := measuredCard()
	sc := tc.PieceAreaScopes["BOMKEY1"]
	sc.Stale = true
	tc.PieceAreaScopes["BOMKEY1"] = sc

	published := publishedEstimates(t, tc)
	if len(published) != 1 {
		t.Fatalf("published %d estimates, want the refusing slot to still be published", len(published))
	}
	e := published[0]
	if e.GetRefusal() != string(entity.AreaEstimateStale) {
		t.Fatalf("refusal = %q, want %q", e.GetRefusal(), entity.AreaEstimateStale)
	}
	if !e.GetStale() {
		t.Error("stale flag is false on a stale scope; the row could not say the measurement is out of date")
	}
	if e.GetPerGarment().GetValue() != "" {
		t.Errorf("a stale slot published a consumption (%q); it would read as measured", e.GetPerGarment().GetValue())
	}
	if e.GetPieceCount() != 1 {
		t.Errorf("piece_count = %d, want 1 — «no pieces» and «pieces measured stale» are different next actions", e.GetPieceCount())
	}
}

// TestPublishedProvenanceNamesTheMeasurement: the estimate is only as good as the parse behind it,
// and the parse conditions decide whether the number is a seam line or a cut line. A row that shows
// metres without saying under which layer and which allowance they were derived invites the operator
// to trust a contour nobody agreed on.
func TestPublishedProvenanceNamesTheMeasurement(t *testing.T) {
	published := publishedEstimates(t, measuredCard())
	if len(published) != 1 {
		t.Fatalf("published %d estimates, want 1", len(published))
	}
	e := published[0]
	if e.GetScopeKey() != "BOMKEY1" {
		t.Errorf("scope_key = %q, want BOMKEY1 — the client joins the per-piece areas by it", e.GetScopeKey())
	}
	if e.GetBomLineKey() != "BOMKEY1" {
		t.Errorf("bom_line_key = %q, want BOMKEY1 — the join key must be the stable one, ids are absent in a release snapshot", e.GetBomLineKey())
	}
	if e.GetContourLayer() != "1" || e.GetSeamAllowanceMm().GetValue() != "10" {
		t.Errorf("measurement conditions lost: layer=%q allowance=%q", e.GetContourLayer(), e.GetSeamAllowanceMm().GetValue())
	}
	if e.GetParsedBy() != "kate" || e.GetParsedAt().AsTime().UTC().Format("2006-01-02") != "2026-08-03" {
		t.Errorf("provenance lost: by=%q at=%v", e.GetParsedBy(), e.GetParsedAt().AsTime())
	}
}

// TestEveryRefusalReachesTheWire.
//
// A refusal that never gets published is a blank cell: the client cannot distinguish «this fabric
// consumes nothing» from «this fabric was not computed and here is the missing fact». Each case
// below is a real card state, and the table is checked against entity.AllAreaEstimateRefusals, so
// adding a tenth reason without giving it a way onto the wire fails here rather than in production.
func TestEveryRefusalReachesTheWire(t *testing.T) {
	type tcase struct {
		name  string
		want  entity.AreaEstimateRefusal
		build func() *entity.TechCard
	}
	cs := []tcase{
		{"no pieces assigned", entity.AreaEstimateNoAssignments, func() *entity.TechCard {
			tc := measuredCard()
			tc.Colorways[0].Usages = nil
			return tc
		}},
		{"no measured areas", entity.AreaEstimateNoAreas, func() *entity.TechCard {
			tc := measuredCard()
			tc.PieceAreaScopes = nil
			return tc
		}},
		{"areas incomplete for size", entity.AreaEstimateIncomplete, func() *entity.TechCard {
			tc := measuredCard()
			sc := tc.PieceAreaScopes["BOMKEY1"]
			sc.Rows = sc.Rows[:1] // size 5 of the declared range has no area
			tc.PieceAreaScopes["BOMKEY1"] = sc
			return tc
		}},
		{"areas stale", entity.AreaEstimateStale, func() *entity.TechCard {
			tc := measuredCard()
			sc := tc.PieceAreaScopes["BOMKEY1"]
			sc.Stale = true
			tc.PieceAreaScopes["BOMKEY1"] = sc
			return tc
		}},
		{"no cutting width", entity.AreaEstimateNoWidth, func() *entity.TechCard {
			tc := measuredCard()
			tc.BomItems[0].FabricWidth = decimal.NullDecimal{}
			return tc
		}},
		{"unit not convertible", entity.AreaEstimateUnitUnknown, func() *entity.TechCard {
			tc := measuredCard()
			tc.BomItems[0].Unit = sql.NullString{String: "kg", Valid: true}
			return tc
		}},
		{"no price", entity.AreaEstimateNoPrice, func() *entity.TechCard {
			tc := measuredCard()
			tc.BomItems[0].UnitPrice = decimal.NullDecimal{}
			return tc
		}},
		{"no basis", entity.AreaEstimateNoBasis, func() *entity.TechCard {
			tc := measuredCard()
			// A run line that names no size: the override says «no basis», never «take the default».
			zero := 0
			tc.CostingSizeOverride = &zero
			return tc
		}},
		// Клеевая полосой по краю (0304) на замере, снятом до 0305: площадь есть, периметра нет.
		// Комплект при этом ПОЛОН и не устарел — то есть все соседние причины сказали бы неправду и
		// отправили оператора чинить то, что в порядке. Единственный слот карточки сделан клеевым,
		// чтобы публикуемая оценка осталась одна: заимствование геометрии здесь ни при чём, контур
		// детали лежит в своём же скоупе.
		{"no measured perimeter", entity.AreaEstimateNoPerimeter, func() *entity.TechCard {
			tc := measuredCard()
			tc.BomItems[0].Section = entity.BomSectionInterlining
			tc.Pieces[0].Fused = true
			tc.Pieces[0].FusingMode = sql.NullString{String: string(entity.PieceFusingModeStrip), Valid: true}
			tc.Pieces[0].FusingWidthMm = decimal.NullDecimal{
				Decimal: decimal.RequireFromString("25"), Valid: true,
			}
			return tc
		}},
		// Полоса БЕЗ СВОЕГО ЧИСЛА при незаданном эталоне: ширины полосы не существует. Комплект
		// полон, замер свеж — недостающий факт лежит в настройках цеха, и только эта причина туда и
		// посылает. До 0328 то же самое состояние записывалось отдельным режимом `seam_allowance`.
		{"no seam allowance standard", entity.AreaEstimateNoStripWidth, func() *entity.TechCard {
			tc := measuredCard()
			tc.BomItems[0].Section = entity.BomSectionInterlining
			tc.Pieces[0].Fused = true
			tc.Pieces[0].FusingMode = sql.NullString{
				String: string(entity.PieceFusingModeStrip), Valid: true,
			}
			tc.Pieces[0].FusingWidthMm = decimal.NullDecimal{}
			return tc
		}},
		{"pin conflict", entity.AreaEstimatePinConflict, func() *entity.TechCard {
			// Two pieces of ONE slot pinned to two different articles: that is two rolls, not an
			// imprecise estimate. Picking one silently would cost half the garment at the other
			// roll's price and the other roll's width.
			tc := measuredCard()
			tc.Pieces = append(tc.Pieces, entity.TechCardPiece{Id: 15, LineKey: "PIECE2", PiecesPerGarment: 1})
			tc.Colorways[0].Usages = []entity.TechCardColorwayUsage{
				{
					Id:         41,
					BomItemId:  sql.NullInt64{Int64: 56, Valid: true},
					PieceId:    sql.NullInt64{Int64: 14, Valid: true},
					MaterialId: sql.NullInt64{Int64: 900, Valid: true},
				},
				{
					Id:         42,
					BomItemId:  sql.NullInt64{Int64: 56, Valid: true},
					PieceId:    sql.NullInt64{Int64: 15, Valid: true},
					MaterialId: sql.NullInt64{Int64: 901, Valid: true},
				},
			}
			return tc
		}},
	}

	covered := map[entity.AreaEstimateRefusal]bool{}
	for _, c := range cs {
		t.Run(c.name, func(t *testing.T) {
			published := publishedEstimates(t, c.build())
			if len(published) != 1 {
				t.Fatalf("published %d estimates, want the refusing slot published exactly once", len(published))
			}
			if got := published[0].GetRefusal(); got != string(c.want) {
				t.Fatalf("refusal = %q, want %q", got, c.want)
			}
			if published[0].GetRefusalText() == "" {
				t.Fatal("refusal_text is empty; the row would show a machine token or nothing at all")
			}
			// «Назначений нет» is the only reason that may report zero assigned pieces. Reporting
			// zero for any other reason sends the operator to assign pieces that are already
			// assigned, instead of to the fact that is actually missing.
			if got := published[0].GetPieceCount(); (got == 0) != (c.want == entity.AreaEstimateNoAssignments) {
				t.Errorf("piece_count = %d under refusal %q", got, c.want)
			}
		})
		covered[c.want] = true
	}
	for _, r := range entity.AllAreaEstimateRefusals {
		if !covered[r] {
			t.Errorf("refusal %q has no case here — nothing proves it can reach a screen", r)
		}
	}
}

// conflictedSecondaryCard: colourway 35 is priced from an authored norm; colourway 36 assigns two
// pieces of the SAME slot to two DIFFERENT articles. The second colourway has a recipe — it just
// cannot be costed from it.
func conflictedSecondaryCard() *entity.TechCard {
	tc := measuredCard()
	tc.Pieces = append(tc.Pieces, entity.TechCardPiece{Id: 15, LineKey: "PIECE2", PiecesPerGarment: 1})
	tc.Colorways[0].Usages = []entity.TechCardColorwayUsage{{
		Id:          34,
		BomItemId:   sql.NullInt64{Int64: 56, Valid: true},
		Consumption: decimal.NullDecimal{Decimal: decimal.RequireFromString("2"), Valid: true},
	}}
	tc.Colorways = append(tc.Colorways, entity.TechCardColorway{
		Id:        36,
		Name:      "ecru",
		ProductId: sql.NullInt32{Int32: 36, Valid: true},
		Usages: []entity.TechCardColorwayUsage{
			{Id: 41, BomItemId: sql.NullInt64{Int64: 56, Valid: true},
				PieceId: sql.NullInt64{Int64: 14, Valid: true}, MaterialId: sql.NullInt64{Int64: 900, Valid: true}},
			{Id: 42, BomItemId: sql.NullInt64{Int64: 56, Valid: true},
				PieceId: sql.NullInt64{Int64: 15, Valid: true}, MaterialId: sql.NullInt64{Int64: 901, Valid: true}},
		},
	})
	return tc
}

// TestConflictedColorwayNeitherComputesNorInheritsAPrice.
//
// A colourway whose slot names two different rolls cannot be costed — that much the refusal already
// says. The trap is what happens NEXT: «cannot be costed» was answered by the rule for «has no
// recipe at all», which hands the colourway the PRIMARY colourway's figure. The seed then writes
// that figure to the second colourway's product and a run plans against it — so the very card the
// server refused to cost still names a price, only somebody else's. Pins exist precisely because
// colours cost differently; inheriting across one is the whole error in one step.
func TestConflictedColorwayNeitherComputesNorInheritsAPrice(t *testing.T) {
	tc := conflictedSecondaryCard()

	if !colorwayHasOwnRecipe(tc, &tc.Colorways[1]) {
		t.Fatal("a conflicted colourway reads as «no recipe»; it would inherit the primary's price and seed it")
	}
	if cost, _ := ComputeColorwayUnitCost(tc, 36, CostingFx{}); cost.Valid {
		t.Fatalf("the conflicted colourway priced itself at %s; nothing here can be costed", cost.Decimal)
	}
	if _, ok := ComputeColorwayCostBreakdownBase(tc, 36, CostingFx{Base: "EUR"}); ok {
		t.Error("a breakdown was produced for a colourway with no computable cost; cost_breakdown would sit under a price that does not exist")
	}
	// ...and the CARD does not stop computing because one colourway is broken: the primary carries
	// an authored norm and must still price the style.
	if cost, _ := ComputeTechCardUnitCost(tc, CostingFx{}); !cost.Valid {
		t.Fatal("the whole card stopped costing because a secondary colourway has arguing pins")
	}
}

// TestCostBlockersNameTheSlotAndTheReason is the visible half of the same finding.
//
// When a refusal makes the cost incomplete, product.cost_price is NOT rewritten — accounting and the
// COGS of everything already sold read it, and blanking it retroactively is worse than leaving it.
// So the card can be released carrying a catalogue price that no longer corresponds to anything, and
// until now the release checklist asked only «is the costing block filled in». The screen has to say
// which fabric and why; «no norm» would send the operator to type a number, which is the opposite of
// the fix.
func TestCostBlockersNameTheSlotAndTheReason(t *testing.T) {
	tc := conflictedSecondaryCard()
	tc.BomItems[0].Name = "основная ткань"

	blockers := TechCardCostBlockers(tc, CostingFx{})
	if len(blockers) != 1 {
		t.Fatalf("blockers = %v, want exactly the one conflicted slot", blockers)
	}
	if !strings.Contains(blockers[0], "основная ткань") || !strings.Contains(blockers[0], "ecru") {
		t.Errorf("blocker %q names neither the fabric nor the colour; the operator cannot find it", blockers[0])
	}
	if !strings.Contains(blockers[0], entity.AreaEstimateRefusalText(entity.AreaEstimatePinConflict)) {
		t.Errorf("blocker %q does not name the reason", blockers[0])
	}

	// An UNMEASURED card is every card in the database today: it must stay silent, or the checklist
	// reds every style at once on deploy day.
	tc.PieceAreaScopes = nil
	if got := TechCardCostBlockers(tc, CostingFx{}); len(got) != 0 {
		t.Fatalf("an unmeasured card reported blockers %v; that is T8's empty recipe, not a failure", got)
	}
}

// TestUnmeasuredSlotStillNamesItsMissingFact.
//
// Every card in the database today is unmeasured, and this is the state the operator meets first.
// The row must not be silent: «площади деталей не измерены» is what sends them to the patterns tab,
// and an empty cell sends them to type a norm by hand — the very thing this program removes.
func TestUnmeasuredSlotStillNamesItsMissingFact(t *testing.T) {
	tc := measuredCard()
	tc.PieceAreaScopes = nil
	published := publishedEstimates(t, tc)
	if len(published) != 1 {
		t.Fatalf("published %d estimates, want 1", len(published))
	}
	if published[0].GetRefusal() != string(entity.AreaEstimateNoAreas) {
		t.Fatalf("refusal = %q, want %q", published[0].GetRefusal(), entity.AreaEstimateNoAreas)
	}
	if published[0].GetParsedAt() != nil {
		t.Error("an unmeasured scope published a parse timestamp; it would read as measured on 1 Jan year 1")
	}
}

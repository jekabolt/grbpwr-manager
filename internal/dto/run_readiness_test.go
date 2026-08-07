package dto

import (
	"database/sql"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// --- фикстура -----------------------------------------------------------------------------------

const (
	rrCard     = 7
	rrBom      = 11
	rrProduct  = 5
	rrMaterial = 100
	rrLineKey  = "LLLLLLLLLLLLLLLLLLLLLLLLLL"
	rrSheetKey = "SSSSSSSSSSSSSSSSSSSSSSSSSS"
	rrSheetURL = "s3://patterns/front.dxf"
)

func rrNullDec(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
}

// rrHealthyCard is a card on which NOTHING is wrong: one fabric slot with an article and a norm, one
// colourway whose recipe takes it from a fully conditioned нормировочная раскладка, one cut piece
// with a DXF alias, one pattern sheet bound to the slot. Each test spoils exactly one thing, so the
// assertion is about that thing and not about the fixture.
func rrHealthyCard() *entity.TechCard {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	pieces := []entity.TechCardPiece{{Id: 1, LineKey: "PIECE1", Name: "перед", PiecesPerGarment: 1}}
	fp := entity.PieceSetFingerprintNull(entity.PieceSetEntriesOf(pieces))
	return &entity.TechCard{
		Id: rrCard,
		TechCardInsert: entity.TechCardInsert{
			Purpose:                 entity.TechCardPurposeSellable,
			SizeIds:                 []int{1, 2},
			RequiredSeamAllowanceCm: rrNullDec("1"),
			Pieces:                  pieces,
			PieceDxfAliases: []entity.TechCardPieceDxfAlias{
				{BomLineKey: rrLineKey, BlockName: "FP", PieceId: 1, PieceLineKey: "PIECE1"},
			},
			Patterns: []entity.TechCardSizePattern{{
				SizeId: 1, LineKey: rrSheetKey, URL: rrSheetURL, Version: 1,
				BomLineKey: sql.NullString{String: rrLineKey, Valid: true},
			}},
			BomItems: []entity.TechCardBomItem{{
				Id: rrBom, LineKey: rrLineKey, Section: entity.BomSectionFabric, Name: "основная ткань",
				MaterialId:      sql.NullInt64{Int64: rrMaterial, Valid: true},
				Unit:            sql.NullString{String: "m", Valid: true},
				FabricDirection: sql.NullString{String: string(entity.FabricDirectionTwoWay), Valid: true},
			}},
			Colorways: []entity.TechCardColorway{{
				Id: 1, Name: "BLK", ProductId: sql.NullInt32{Int32: rrProduct, Valid: true},
				Usages: []entity.TechCardColorwayUsage{{
					BomItemId:         sql.NullInt64{Int64: rrBom, Valid: true},
					Consumption:       rrNullDec("1.5"),
					ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceMarker, Valid: true},
				}},
			}},
		},
		Markers: []entity.TechCardMarkerSummary{{
			Id: 21, TechCardId: rrCard, Name: "норма основной",
			BomItemId:     sql.NullInt64{Int64: rrBom, Valid: true},
			IsNorm:        true,
			FabricWidthCm: decimal.RequireFromString("140"),
			// Conditions recorded, confirmed, and clearing the card's 1 cm standard.
			SeamAllowanceCm:    rrNullDec("1"),
			ContourAllowanceCm: rrNullDec("0"),
			AllowFlip:          sql.NullBool{Bool: false, Valid: true},
			PieceSetFp:         fp,
			CardPieceSetFp:     fp,
			UpdatedAt:          now,
		}},
		LinkedMaterials: map[int]entity.MaterialWithPrice{
			rrMaterial: {Material: entity.Material{Id: rrMaterial, MaterialInsert: entity.MaterialInsert{
				Name: "твил 320", Unit: sql.NullString{String: "m", Valid: true},
				FabricAttr: &entity.MaterialFabricAttr{
					WidthCm: rrNullDec("150"), SelvedgeCm: decimal.RequireFromString("1"),
				},
			}}},
		},
	}
}

func rrInput(card *entity.TechCard) RunReadinessInput {
	return RunReadinessInput{
		Card:        card,
		ColorwayIds: []int{rrProduct},
		Cells:       []entity.RunReadinessCell{{ColorwayId: rrProduct, SizeId: 1, PlannedQty: 10}},
		Settings:    &entity.WorkshopSettings{},
		OnHand:      map[int]decimal.Decimal{rrMaterial: decimal.RequireFromString("1000")},
	}
}

func rrKeys(res RunReadinessResult) map[string]entity.RunReadinessSeverity {
	out := map[string]entity.RunReadinessSeverity{}
	for _, f := range res.Report.Card {
		out[f.Key] = f.Severity
	}
	for _, c := range res.Report.Colorways {
		for _, f := range c.Findings {
			out[f.Key] = f.Severity
		}
	}
	for _, f := range res.Report.Run {
		out[f.Key] = f.Severity
	}
	return out
}

func rrFind(res RunReadinessResult, key string) (entity.RunReadinessFinding, bool) {
	var found entity.RunReadinessFinding
	ok := false
	walk := func(rows []entity.RunReadinessFinding) {
		for _, f := range rows {
			if f.Key == key {
				found, ok = f, true
			}
		}
	}
	walk(res.Report.Card)
	for _, c := range res.Report.Colorways {
		walk(c.Findings)
	}
	walk(res.Report.Run)
	return found, ok
}

// --- тесты --------------------------------------------------------------------------------------

// TestRunReadinessEmitsEveryCheckIncludingThePassingOnes is the acceptance probe «ни один ключ не
// пропадает из ответа». A response listing only refusals never tells the operator WHAT the gate
// checks — and learning that on the day blocking is switched on is exactly the surprise report-only
// mode exists to prevent.
func TestRunReadinessEmitsEveryCheckIncludingThePassingOnes(t *testing.T) {
	res := ComputeProductionRunReadiness(rrInput(rrHealthyCard()))
	got := rrKeys(res)
	want := []string{
		entity.RunReadinessKeyReleaseFrozen, entity.RunReadinessKeyCardSizeRange,
		entity.RunReadinessKeyCardPieces, entity.RunReadinessKeyCardPiecesDxfMatched,
		entity.RunReadinessKeyPatternBindingResolved,
		entity.RunReadinessKeyColorwayLive, entity.RunReadinessKeySlotArticle, entity.RunReadinessKeySlotNorm,
		entity.RunReadinessKeyNormProvenance, entity.RunReadinessKeyNormMultiple,
		entity.RunReadinessKeyNormConditionsRecorded, entity.RunReadinessKeyNormSeamAllowance,
		entity.RunReadinessKeyNormFlipPolicy, entity.RunReadinessKeyNormPieceSet,
		entity.RunReadinessKeyNormWidthVsArticle,
		entity.RunReadinessKeySizesInRange, entity.RunReadinessKeySizesInDxf,
		entity.RunReadinessKeyQuantitiesPresent, entity.RunReadinessKeyStockShortage,
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("key %q is missing from the response — every check is listed, including the passing ones", k)
		}
	}
	// Every key emitted must be in the registry: an unregistered key is one no client map can route.
	for k := range got {
		if _, ok := entity.RunReadinessKeyGroups[k]; !ok {
			t.Errorf("emitted key %q is not in entity.RunReadinessKeyGroups", k)
		}
	}
	if !res.Report.Ready() {
		t.Fatalf("the healthy fixture must be ready; blockers: %v", res.Report.Blockers())
	}
	// A row that PASSED carries no detail; a row that did not carries one. Both halves are checked,
	// because a passing row with a detail reads as a failure that was let through, and a failing row
	// without one is a refusal the operator cannot act on.
	assertDetailDiscipline(t, res)
}

func assertDetailDiscipline(t *testing.T, res RunReadinessResult) {
	t.Helper()
	pb := RunReadinessToPb(res)
	check := func(key, detail string, isOK bool) {
		if isOK && detail != "" {
			t.Errorf("row %q passed but carries a detail (%q) — detail is the explanation of a failure", key, detail)
		}
		if !isOK && detail == "" {
			t.Errorf("row %q did not pass but says nothing about why", key)
		}
	}
	for _, f := range pb.GetCard() {
		check(f.GetKey(), f.GetDetail(), f.GetSeverity() == 1)
	}
	for _, c := range pb.GetColorways() {
		for _, f := range c.GetFindings() {
			check(f.GetKey(), f.GetDetail(), f.GetSeverity() == 1)
		}
	}
	for _, f := range pb.GetRun() {
		check(f.GetKey(), f.GetDetail(), f.GetSeverity() == 1)
	}
}

// TestRunReadinessAuxCardShortCircuits: an auxiliary card produces a MATERIAL, has no colourways by
// construction, and the cloth-norm vocabulary does not apply to it. The filter is on the SERVER
// because the create-time re-check is on the server — a client-side filter would leave aux runs
// blockable through the API.
func TestRunReadinessAuxCardShortCircuits(t *testing.T) {
	card := rrHealthyCard()
	card.Purpose = entity.TechCardPurposeAuxiliary
	// Spoil everything a sellable card would be refused for; none of it may matter.
	card.SizeIds = nil
	card.Pieces = nil
	card.Colorways = nil
	in := rrInput(card)
	in.Settings = &entity.WorkshopSettings{RunReadinessBlocking: sql.NullBool{Bool: true, Valid: true}}
	res := ComputeProductionRunReadiness(in)
	if !res.Report.Ready() || res.Report.WouldBlock() {
		t.Fatalf("an auxiliary card is never gated; blockers: %v", res.Report.Blockers())
	}
	if len(res.Report.Card) != 1 || res.Report.Card[0].Key != entity.RunReadinessKeyCardAuxiliary {
		t.Fatalf("expected exactly the card_auxiliary row, got %+v", res.Report.Card)
	}
	if len(res.Report.Colorways) != 0 || len(res.Report.Run) != 0 {
		t.Fatal("the aux short circuit emits no colourway and no run rows at all")
	}
}

// TestRunReadinessReportOnlyVsBlocking: the VERDICT is identical in both modes and only would_block
// moves. That is what makes switching blocking on «one control stops being clickable» rather than
// «a new screen appeared».
func TestRunReadinessReportOnlyVsBlocking(t *testing.T) {
	card := rrHealthyCard()
	card.SizeIds = nil // card_size_range → BLOCKER

	report := ComputeProductionRunReadiness(rrInput(card))
	if report.Report.Ready() {
		t.Fatal("an empty size range must block")
	}
	if report.Report.WouldBlock() {
		t.Fatal("report-only never refuses")
	}

	in := rrInput(card)
	in.Settings = &entity.WorkshopSettings{RunReadinessBlocking: sql.NullBool{Bool: true, Valid: true}}
	blocking := ComputeProductionRunReadiness(in)
	if !blocking.Report.WouldBlock() {
		t.Fatal("blocking mode refuses on a blocker")
	}
	if len(rrKeys(report)) != len(rrKeys(blocking)) {
		t.Fatal("the two modes must produce the SAME rows — only would_block differs")
	}
}

// TestRunReadinessNormMultipleWarnsAndNamesBoth is the Ф3 Р2 condition: exclusivity is held by a
// transaction rather than a UNIQUE index, so two norms on one cloth ARE possible, and the one thing
// that may not happen is silently taking the first.
func TestRunReadinessNormMultipleWarnsAndNamesBoth(t *testing.T) {
	card := rrHealthyCard()
	older := card.Markers[0]
	older.Id = 20
	older.Name = "старая норма"
	older.UpdatedAt = card.Markers[0].UpdatedAt.Add(-time.Hour)
	card.Markers = append(card.Markers, older)

	res := ComputeProductionRunReadiness(rrInput(card))
	f, ok := rrFind(res, entity.RunReadinessKeyNormMultiple)
	if !ok {
		t.Fatal("norm_multiple must be emitted")
	}
	if f.Severity != entity.RunReadinessWarning {
		t.Fatalf("norm_multiple = %s, want warning: the tiebreak is deterministic, so the run is possible", f.Severity)
	}
	if !strings.Contains(f.Detail, "#21") || !strings.Contains(f.Detail, "#20") {
		t.Fatalf("the detail must name BOTH norms, got %q", f.Detail)
	}
	if f.Target.MarkerId != 21 {
		t.Fatalf("target points at the WINNER (newest by updated_at), got marker %d", f.Target.MarkerId)
	}
	if !res.Report.Ready() {
		t.Fatal("two norms is a warning, not a refusal")
	}
	// Determinism: the same input answers the same way every time, so two screens never disagree.
	again, _ := rrFind(ComputeProductionRunReadiness(rrInput(card)), entity.RunReadinessKeyNormMultiple)
	if again.Target.MarkerId != f.Target.MarkerId {
		t.Fatal("the norm tiebreak must be deterministic across calls")
	}
}

// TestRunReadinessManualNormIsAcceptedAsAnEscapeHatch: without it the first strange DXF stops
// production. It is a WARNING, and the five marker-condition rows are NOT emitted — there is no
// раскладка to judge, and printing five «no verdict» rows about one would read as five problems.
func TestRunReadinessManualNormIsAcceptedAsAnEscapeHatch(t *testing.T) {
	card := rrHealthyCard()
	card.Colorways[0].Usages[0].ConsumptionSource = sql.NullString{String: "manual", Valid: true}

	res := ComputeProductionRunReadiness(rrInput(card))
	f, _ := rrFind(res, entity.RunReadinessKeyNormProvenance)
	if f.Severity != entity.RunReadinessWarning {
		t.Fatalf("a manual norm is accepted (warning), got %s", f.Severity)
	}
	if !res.Report.Ready() {
		t.Fatalf("a manual norm does not refuse a run; blockers: %v", res.Report.Blockers())
	}
	for _, k := range []string{
		entity.RunReadinessKeyNormConditionsRecorded, entity.RunReadinessKeyNormSeamAllowance,
		entity.RunReadinessKeyNormFlipPolicy, entity.RunReadinessKeyNormPieceSet,
		entity.RunReadinessKeyNormWidthVsArticle, entity.RunReadinessKeyNormMultiple,
	} {
		if _, ok := rrFind(res, k); ok {
			t.Errorf("%q must not be emitted when there is no раскладка to judge", k)
		}
	}
}

// TestRunReadinessMissingNormSuppressesTheConditionRows is invariant 1 stated as a test: one fact
// goes red ONCE.
func TestRunReadinessMissingNormSuppressesTheConditionRows(t *testing.T) {
	card := rrHealthyCard()
	card.Markers = nil // the recipe still says «from a marker», but there is no norm marker

	res := ComputeProductionRunReadiness(rrInput(card))
	f, _ := rrFind(res, entity.RunReadinessKeyNormProvenance)
	if f.Severity != entity.RunReadinessBlocker {
		t.Fatalf("norm_provenance = %s, want blocker", f.Severity)
	}
	blockers := res.Report.Blockers()
	if len(blockers) != 1 || blockers[0].Key != entity.RunReadinessKeyNormProvenance {
		t.Fatalf("exactly ONE row may go red about one missing norm, got %v", blockers)
	}
}

// TestRunReadinessOrphanedNormIsNamed: fk_tcm_bom is ON DELETE SET NULL, so deleting a BOM line
// moves its norm into the «no cloth» scope instead of destroying it. Without this sentence the
// operator cannot find their раскладка and re-designates a new one over the top.
func TestRunReadinessOrphanedNormIsNamed(t *testing.T) {
	card := rrHealthyCard()
	card.Markers[0].BomItemId = sql.NullInt64{} // the slot was deleted; the norm survived, unbound

	res := ComputeProductionRunReadiness(rrInput(card))
	f, _ := rrFind(res, entity.RunReadinessKeyNormProvenance)
	if f.Severity != entity.RunReadinessBlocker {
		t.Fatalf("norm_provenance = %s, want blocker", f.Severity)
	}
	if !strings.Contains(f.Detail, "БЕЗ ТКАНИ") {
		t.Fatalf("the detail must say where the norm went, got %q", f.Detail)
	}
}

// TestRunReadinessSizeIndexStatesAreAllUnknown is acceptance probes 10 and 11 together: a re-upload
// stales the index (it must NOT stay green) and an empty token set is a LEGAL answer (it must NOT
// turn into a blocker on every size).
func TestRunReadinessSizeIndexStatesAreAllUnknown(t *testing.T) {
	card := rrHealthyCard()
	scopeSheets := []entity.PatternSheetRef{{LineKey: rrSheetKey, URL: rrSheetURL, Version: 1}}
	current := entity.PatternSheetFingerprint(scopeSheets)

	tests := []struct {
		name  string
		index map[string]entity.PatternSizeIndexRow
		want  string // a fragment the detail must contain
	}{
		{"nobody ran the audit", nil, "не проверялись"},
		{
			"the files changed after the parse — a re-upload must stale the index, not leave it green",
			map[string]entity.PatternSizeIndexRow{rrLineKey: {
				ScopeKey: rrLineKey, SheetFingerprint: "fingerprint-of-the-old-files", SizeTokensJSON: `["s","m","l"]`,
			}},
			"менялись",
		},
		{
			"an empty token set is legal and means the files carry no size coding",
			map[string]entity.PatternSizeIndexRow{rrLineKey: {
				ScopeKey: rrLineKey, SheetFingerprint: current, SizeTokensJSON: `[]`,
			}},
			"размерного кодирования",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := rrInput(card)
			in.PatternSizeIndex = tt.index
			res := ComputeProductionRunReadiness(in)
			f, ok := rrFind(res, entity.RunReadinessKeySizesInDxf)
			if !ok {
				t.Fatal("sizes_in_dxf must always be emitted")
			}
			if f.Severity != entity.RunReadinessUnknown {
				t.Fatalf("sizes_in_dxf = %s, want unknown — an absent, stale or ungraded index is a missing INSTRUMENT", f.Severity)
			}
			if !strings.Contains(f.Detail, tt.want) {
				t.Fatalf("detail %q must say which of the three reasons applies (%q)", f.Detail, tt.want)
			}
			if !res.Report.Ready() {
				t.Fatalf("an UNKNOWN size index must never refuse a run; blockers: %v", res.Report.Blockers())
			}
		})
	}
}

// TestRunReadinessForeignColorwayBlocks: nothing on the plan path checks this today —
// validateRunLineVariants only looks at aux colours — so a run planned against a foreign product id
// is accepted by the store. The gate is where that first becomes visible.
func TestRunReadinessForeignColorwayBlocks(t *testing.T) {
	in := rrInput(rrHealthyCard())
	in.ColorwayIds = []int{999}
	in.Cells = []entity.RunReadinessCell{{ColorwayId: 999, SizeId: 1, PlannedQty: 10}}

	res := ComputeProductionRunReadiness(in)
	f, _ := rrFind(res, entity.RunReadinessKeyColorwayLive)
	if f.Severity != entity.RunReadinessBlocker {
		t.Fatalf("colorway_live = %s, want blocker", f.Severity)
	}
	// Invariant 1 again: a product that is not this card's colourway has no recipe here, so the
	// twelve rows about a recipe are not printed about it.
	if len(res.Report.Colorways) != 1 || len(res.Report.Colorways[0].Findings) != 1 {
		t.Fatalf("a foreign colourway emits ONE row, got %+v", res.Report.Colorways)
	}
	if res.Report.Colorways[0].Ready() {
		t.Fatal("a foreign colourway is not ready — this is what greys its checkbox")
	}
}

// TestRunReadinessSizeOutOfRangeBlocks + the colourless-line exemption.
func TestRunReadinessSizeOutOfRange(t *testing.T) {
	in := rrInput(rrHealthyCard())
	in.Cells = append(in.Cells, entity.RunReadinessCell{ColorwayId: rrProduct, SizeId: 99, PlannedQty: 3})
	res := ComputeProductionRunReadiness(in)
	f, _ := rrFind(res, entity.RunReadinessKeySizesInRange)
	if f.Severity != entity.RunReadinessBlocker || f.Target.SizeId != 99 {
		t.Fatalf("sizes_in_range = %s target %d, want blocker on 99", f.Severity, f.Target.SizeId)
	}

	// A cell naming NO size is not «size 0 out of range»: it names no size at all.
	in2 := rrInput(rrHealthyCard())
	in2.Cells = []entity.RunReadinessCell{{ColorwayId: rrProduct, SizeId: 0, PlannedQty: 3}}
	f2, _ := rrFind(ComputeProductionRunReadiness(in2), entity.RunReadinessKeySizesInRange)
	if f2.Severity != entity.RunReadinessOK {
		t.Fatalf("a sizeless cell must not be reported out of range, got %s (%s)", f2.Severity, f2.Detail)
	}
}

// TestRunReadinessWidthBlockerNamesTheBranch: «the narrowest available roll measures 148» and «the
// article's nominal less the кромка is 146» send an operator to two different actions, so the
// sentence has to say which one answered.
func TestRunReadinessWidthBlockerNamesTheBranch(t *testing.T) {
	card := rrHealthyCard()
	card.Markers[0].FabricWidthCm = decimal.RequireFromString("149") // article gives 150−2×1 = 148

	res := ComputeProductionRunReadiness(rrInput(card))
	f, _ := rrFind(res, entity.RunReadinessKeyNormWidthVsArticle)
	if f.Severity != entity.RunReadinessBlocker {
		t.Fatalf("width = %s, want blocker", f.Severity)
	}
	if !strings.Contains(f.Detail, "номинал") {
		t.Fatalf("with no measured lots the detail must say it used the catalogue width, got %q", f.Detail)
	}

	in := rrInput(card)
	in.NarrowestMeasuredLotCm = map[int]decimal.NullDecimal{rrMaterial: rrNullDec("160")}
	f2, _ := rrFind(ComputeProductionRunReadiness(in), entity.RunReadinessKeyNormWidthVsArticle)
	if f2.Severity != entity.RunReadinessOK {
		t.Fatalf("a wide measured lot (160 − 2 = 158) clears a 149 cm marker, got %s (%s)", f2.Severity, f2.Detail)
	}
}

// TestRunReadinessSlotBlockersComeFromTheMaterialPlan: slot_article/slot_norm are READ BACK from the
// plan by its machine key, not recomputed. This test spoils the recipe and asserts the gate follows.
func TestRunReadinessSlotBlockersComeFromTheMaterialPlan(t *testing.T) {
	card := rrHealthyCard()
	card.BomItems[0].MaterialId = sql.NullInt64{} // no slot default, and the colourway pins nothing

	res := ComputeProductionRunReadiness(rrInput(card))
	f, _ := rrFind(res, entity.RunReadinessKeySlotArticle)
	if f.Severity != entity.RunReadinessBlocker {
		t.Fatalf("slot_article = %s, want blocker", f.Severity)
	}
	if !strings.Contains(f.Detail, "основная ткань") {
		t.Fatalf("the detail must name the slot, got %q", f.Detail)
	}
	if f.Target.BomItemId != rrBom {
		t.Fatalf("the target must point at the offending slot, got %d", f.Target.BomItemId)
	}

	card2 := rrHealthyCard()
	card2.Colorways[0].Usages = nil // the slot exists but the recipe never references it
	res2 := ComputeProductionRunReadiness(rrInput(card2))
	f2, _ := rrFind(res2, entity.RunReadinessKeySlotNorm)
	if f2.Severity != entity.RunReadinessBlocker {
		t.Fatalf("slot_norm = %s, want blocker on an unreferenced slot", f2.Severity)
	}
}

// TestRunReadinessUnitCoverageCountsGarmentsNotCloth: a garment is not provisioned until ALL of its
// cloths are.
func TestRunReadinessUnitCoverageCountsGarmentsNotCloth(t *testing.T) {
	card := rrHealthyCard()
	// A second cloth the recipe never mentions: the garment's need for it is not in the plan.
	card.BomItems = append(card.BomItems, entity.TechCardBomItem{
		Id: 12, LineKey: "MMMMMMMMMMMMMMMMMMMMMMMMMM", Section: entity.BomSectionLining, Name: "подкладка",
		MaterialId: sql.NullInt64{Int64: 101, Valid: true},
		Unit:       sql.NullString{String: "m", Valid: true},
	})
	res := ComputeProductionRunReadiness(rrInput(card))
	if len(res.UnitCoverage) != 1 {
		t.Fatalf("one planned cell, one unit-coverage row; got %d", len(res.UnitCoverage))
	}
	row := res.UnitCoverage[0]
	if row.GetProvisionedQty() != 0 {
		t.Fatalf("provisioned = %d, want 0: one cloth without a norm means no garment is provisioned", row.GetProvisionedQty())
	}
	if len(row.GetBlockingBomItemIds()) != 1 || row.GetBlockingBomItemIds()[0] != 12 {
		t.Fatalf("the blocking slot must be named, got %v", row.GetBlockingBomItemIds())
	}
	if row.GetPlannedQty() != 10 {
		t.Fatalf("planned = %d, want 10", row.GetPlannedQty())
	}

	// With the lining recipe present, the whole cell is provisioned.
	card.Colorways[0].Usages = append(card.Colorways[0].Usages, entity.TechCardColorwayUsage{
		BomItemId:   sql.NullInt64{Int64: 12, Valid: true},
		Consumption: rrNullDec("1"),
	})
	in := rrInput(card)
	in.OnHand[101] = decimal.RequireFromString("4") // only four garments' worth of lining on the shelf
	res2 := ComputeProductionRunReadiness(in)
	row2 := res2.UnitCoverage[0]
	if row2.GetProvisionedQty() != 10 {
		t.Fatalf("provisioned = %d, want 10 — the recipe now covers every slot", row2.GetProvisionedQty())
	}
	// units_from_stock is the fraction that DOES exist: min over slots of floor(on_hand / norm).
	if row2.GetUnitsFromStock() != 4 {
		t.Fatalf("units_from_stock = %d, want 4 (the lining is the binding slot)", row2.GetUnitsFromStock())
	}
}

// TestRunReadinessCellsAreOrderIndependent: two identical requests must produce identical answers,
// so a client can cache on the request hash.
func TestRunReadinessCellsAreOrderIndependent(t *testing.T) {
	a := rrInput(rrHealthyCard())
	a.Cells = []entity.RunReadinessCell{
		{ColorwayId: rrProduct, SizeId: 2, PlannedQty: 4},
		{ColorwayId: rrProduct, SizeId: 1, PlannedQty: 6},
	}
	b := rrInput(rrHealthyCard())
	b.Cells = []entity.RunReadinessCell{a.Cells[1], a.Cells[0]}

	ra, rb := ComputeProductionRunReadiness(a), ComputeProductionRunReadiness(b)
	if len(ra.UnitCoverage) != len(rb.UnitCoverage) {
		t.Fatal("unit coverage row count must not depend on cell order")
	}
	for i := range ra.UnitCoverage {
		if ra.UnitCoverage[i].GetSizeId() != rb.UnitCoverage[i].GetSizeId() {
			t.Fatalf("unit coverage order differs at %d", i)
		}
	}
	ka, kb := sortedKeys(rrKeys(ra)), sortedKeys(rrKeys(rb))
	if strings.Join(ka, ",") != strings.Join(kb, ",") {
		t.Fatal("the key set must not depend on cell order")
	}
}

func sortedKeys(m map[string]entity.RunReadinessSeverity) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

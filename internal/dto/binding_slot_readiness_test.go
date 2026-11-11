package dto

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Owner decisions on bias binding (slots audit §6), read side: a roll-goods line without a pattern
// file is not an error anywhere, and readiness text names a slot the way the operator sees it.

const bindLineKey = "BBBBBBBBBBBBBBBBBBBBBBBBBB"

// withSheetlessBindingLine adds a bias-binding roll-goods line that carries no pattern file: the
// article, unit, direction and a norm computed from the cut length of its strips (how bias binding is
// costed) are set so that nothing but the missing sheet could be flagged.
func withSheetlessBindingLine(card *entity.TechCard) *entity.TechCard {
	card.BomItems = append(card.BomItems, entity.TechCardBomItem{
		Id: rrBom + 1, LineKey: bindLineKey, Section: entity.BomSectionLining, Name: "bias tape",
		Purpose:         sql.NullString{String: string(entity.BomPurposeBinding), Valid: true},
		MaterialId:      sql.NullInt64{Int64: rrMaterial, Valid: true},
		Unit:            sql.NullString{String: "m", Valid: true},
		FabricDirection: sql.NullString{String: string(entity.FabricDirectionTwoWay), Valid: true},
	})
	card.Colorways[0].Usages = append(card.Colorways[0].Usages, entity.TechCardColorwayUsage{
		BomItemId:         sql.NullInt64{Int64: rrBom + 1, Valid: true},
		Consumption:       rrNullDec("0.3"),
		ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceCutLength, Valid: true},
	})
	return card
}

// TestSheetlessBindingLineFlagsNothing — §6.1: no check flags a roll-goods line without a sheet, and
// a binding line is never flagged for it. Readiness, the pattern-size verdict and the cost blockers
// must read the card exactly as they read it without that line.
func TestSheetlessBindingLineFlagsNothing(t *testing.T) {
	index := func(card *entity.TechCard) map[string]entity.PatternSizeIndexRow {
		scopes := patternScopesOfCard(card)
		if len(scopes) != 1 {
			t.Fatalf("want exactly the main fabric's scope, got %+v", scopes)
		}
		return map[string]entity.PatternSizeIndexRow{scopes[0].key: {
			ScopeKey: scopes[0].key, SheetFingerprint: entity.PatternSheetFingerprint(scopes[0].sheets),
			SizeTokensJSON: `["s","m"]`,
		}}
	}
	for _, tt := range []struct {
		name    string
		indexed bool
	}{{"size index missing", false}, {"size index fresh", true}} {
		t.Run(tt.name, func(t *testing.T) {
			base, withBind := rrHealthyCard(), withSheetlessBindingLine(rrHealthyCard())
			inBase, inBind := rrInput(base), rrInput(withBind)
			if tt.indexed {
				inBase.PatternSizeIndex, inBind.PatternSizeIndex = index(base), index(withBind)
			}
			resBase, resBind := ComputeProductionRunReadiness(inBase), ComputeProductionRunReadiness(inBind)
			if got, want := rrKeys(resBind), rrKeys(resBase); !reflect.DeepEqual(got, want) {
				t.Fatalf("a sheetless binding line changed readiness:\n got %v\nwant %v", got, want)
			}
			// The pattern rows read the card exactly as before, word for word.
			for _, key := range []string{entity.RunReadinessKeySizesInDxf, entity.RunReadinessKeyPatternBindingResolved,
				entity.RunReadinessKeyCardPiecesDxfMatched} {
				fb, okB := rrFind(resBase, key)
				fw, okW := rrFind(resBind, key)
				if okB != okW || fb.Severity != fw.Severity || fb.Detail != fw.Detail {
					t.Errorf("%s changed with a sheetless binding line: %s %q → %s %q", key, fb.Severity, fb.Detail, fw.Severity, fw.Detail)
				}
			}
			// And no row that is not OK is about the binding line at all.
			all := append(append([]entity.RunReadinessFinding{}, resBind.Report.Card...), resBind.Report.Run...)
			for _, c := range resBind.Report.Colorways {
				all = append(all, c.Findings...)
			}
			for _, f := range all {
				if f.Severity != entity.RunReadinessOK &&
					(strings.Contains(f.Detail, "bias tape") || strings.Contains(f.Detail, bindLineKey)) {
					t.Errorf("%s (%s) flags the sheetless binding line: %q", f.Key, f.Severity, f.Detail)
				}
			}
			okB, unkB, missB := TechCardPatternSizeVerdict(base, inBase.PatternSizeIndex)
			okW, unkW, missW := TechCardPatternSizeVerdict(withBind, inBind.PatternSizeIndex)
			if okB != okW || unkB != unkW || !reflect.DeepEqual(missB, missW) {
				t.Fatalf("pattern-size verdict changed: (%v %q %v) → (%v %q %v)", okB, unkB, missB, okW, unkW, missW)
			}
		})
	}

	// A MEASURED card (the cost blockers are silent on an unmeasured one by design): the binding line
	// has pieces assigned to it in the recipe but no file and no areas. «Not measured» is not a
	// blocker, so the line must not appear.
	tc := measuredCard()
	if got := TechCardCostBlockers(tc, CostingFx{}); len(got) != 0 {
		t.Fatalf("fixture: the measured card must be clean, got %v", got)
	}
	tc.BomItems = append(tc.BomItems, entity.TechCardBomItem{
		Id: 77, LineKey: bindLineKey, Section: entity.BomSectionLining, Name: "bias tape",
		Purpose:     sql.NullString{String: string(entity.BomPurposeBinding), Valid: true},
		Unit:        sql.NullString{String: "m", Valid: true},
		UnitPrice:   rrNullDec("10"),
		Currency:    sql.NullString{String: "EUR", Valid: true},
		FabricWidth: rrNullDec("100"),
	})
	if got := TechCardCostBlockers(tc, CostingFx{}); len(got) != 0 {
		t.Fatalf("a sheetless binding line raised cost blockers %v", got)
	}
	tc.Colorways[0].Usages = append(tc.Colorways[0].Usages, entity.TechCardColorwayUsage{
		BomItemId: sql.NullInt64{Int64: 77, Valid: true}, PieceId: sql.NullInt64{Int64: int64(tc.Pieces[0].Id), Valid: true},
	})
	if got := TechCardCostBlockers(tc, CostingFx{}); len(got) != 0 {
		t.Fatalf("an unmeasured binding slot with assigned pieces raised cost blockers %v", got)
	}
}

// TestReadinessNamesALineScopedSlotByItsMaterial: the scope of an unsorted line (and of an interlining
// line) is keyed by the line's ULID. Readiness text is operator text: it names the material, never
// the key — in the run gate and in the card checklist verdict alike.
func TestReadinessNamesALineScopedSlotByItsMaterial(t *testing.T) {
	card := rrHealthyCard()
	stale := map[string]entity.PatternSizeIndexRow{rrLineKey: {
		ScopeKey: rrLineKey, SheetFingerprint: "fingerprint-of-the-old-files", SizeTokensJSON: `["s"]`,
	}}
	for _, index := range []map[string]entity.PatternSizeIndexRow{nil, stale} {
		in := rrInput(card)
		in.PatternSizeIndex = index
		f, ok := rrFind(ComputeProductionRunReadiness(in), entity.RunReadinessKeySizesInDxf)
		if !ok {
			t.Fatal("sizes_in_dxf must always be emitted")
		}
		if !strings.Contains(f.Detail, "основная ткань") || strings.Contains(f.Detail, rrLineKey) {
			t.Errorf("run gate detail %q must name the fabric by its material, not by its line key", f.Detail)
		}
		_, unknown, _ := TechCardPatternSizeVerdict(card, index)
		if !strings.Contains(unknown, "основная ткань") || strings.Contains(unknown, rrLineKey) {
			t.Errorf("checklist verdict %q must name the fabric by its material, not by its line key", unknown)
		}
	}

	// A purpose-scoped slot keeps its purpose word.
	sorted := rrHealthyCard()
	sorted.BomItems[0].Purpose = sql.NullString{String: "main", Valid: true}
	if _, unknown, _ := TechCardPatternSizeVerdict(sorted, nil); !strings.HasPrefix(unknown, "main: ") {
		t.Errorf("purpose slot verdict %q must lead with the purpose word", unknown)
	}

	// A sheet still bound to a line that left the BOM is named as such, never by the dead key.
	orphan := rrHealthyCard()
	orphan.Patterns[0].BomLineKey = sql.NullString{String: "GGGGGGGGGGGGGGGGGGGGGGGGGG", Valid: true}
	if _, unknown, _ := TechCardPatternSizeVerdict(orphan, nil); strings.Contains(unknown, "GGGGGGGGGGGGGGGGGGGGGGGGGG") ||
		!strings.Contains(unknown, "fabric no longer in BOM") {
		t.Errorf("orphan sheet verdict %q must not print the dead line key", unknown)
	}
}

// TestBindingSheetIsAnOrdinarySlotInReadiness — §6.2, read side: a sheet bound to the binding purpose
// is a pattern slot like any other. Its size index is asked for under «binding», an unparsed one is
// reported under that word, and a fresh one counts exactly like the main fabric's.
func TestBindingSheetIsAnOrdinarySlotInReadiness(t *testing.T) {
	card := withSheetlessBindingLine(rrHealthyCard())
	card.Patterns = append(card.Patterns, entity.TechCardSizePattern{
		SizeId: 1, LineKey: "BSBSBSBSBSBSBSBSBSBSBSBSBS", URL: "s3://patterns/bias.dxf", Version: 1,
		FabricPurpose: sql.NullString{String: string(entity.BomPurposeBinding), Valid: true},
	})
	scopes := patternScopesOfCard(card)
	keys := make([]string, 0, len(scopes))
	for _, sc := range scopes {
		keys = append(keys, sc.key)
	}
	if !reflect.DeepEqual(keys, []string{rrLineKey, "binding"}) && !reflect.DeepEqual(keys, []string{"binding", rrLineKey}) {
		t.Fatalf("pattern scopes = %v, want the main line and «binding»", keys)
	}
	fresh := map[string]entity.PatternSizeIndexRow{}
	for _, sc := range scopes {
		fresh[sc.key] = entity.PatternSizeIndexRow{
			ScopeKey: sc.key, SheetFingerprint: entity.PatternSheetFingerprint(sc.sheets), SizeTokensJSON: `[]`,
		}
	}
	onlyMain := map[string]entity.PatternSizeIndexRow{rrLineKey: fresh[rrLineKey]}
	_, unknown, _ := TechCardPatternSizeVerdict(card, onlyMain)
	if !strings.Contains(unknown, "binding: sizes in the files were never checked") {
		t.Fatalf("an unparsed binding sheet must be reported under its purpose word, got %q", unknown)
	}
	in := rrInput(card)
	in.PatternSizeIndex = fresh
	f, _ := rrFind(ComputeProductionRunReadiness(in), entity.RunReadinessKeySizesInDxf)
	if strings.Contains(f.Detail, "never checked") || strings.Contains(f.Detail, "changed after the check") {
		t.Fatalf("a fresh binding index must be found and read as current, got %s %q", f.Severity, f.Detail)
	}
}

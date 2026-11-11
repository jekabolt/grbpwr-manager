package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// Slots audit, server lane (a): a card save — a BOM purpose re-sort above all — never deletes measured
// areas or the size index. Only an unambiguous sheet move into a vacant scope (and the raw key of a
// line that was just sorted) carries them along; a split, a merge into a scope that already holds
// rows and a bucket no line exposes any more stay PARKED under the old key, so sorting back gives
// them back as current data. Side writes stay real (Put/Save), so every fingerprint is the server's.

func parkNS(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }

// parkRows counts one scope's rows in the size index and in the measured areas.
func parkRows(t *testing.T, ctx context.Context, cardID int, scope string) [2]int {
	t.Helper()
	return [2]int{
		sheetBindingScopeRows(t, ctx, "tech_card_pattern_size_index", cardID, scope),
		sheetBindingScopeRows(t, ctx, "tech_card_piece_area", cardID, scope),
	}
}

// parkTotals counts every sidecar row of the card, whatever key it sits under.
func parkTotals(t *testing.T, ctx context.Context, cardID int) [2]int {
	t.Helper()
	var out [2]int
	for i, table := range []string{"tech_card_pattern_size_index", "tech_card_piece_area"} {
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM "+table+" WHERE tech_card_id = ?", cardID).Scan(&out[i]))
	}
	return out
}

// parkMeasure writes a scope's size index and its areas (every piece ungraded, one row each) through
// the side RPCs the panel calls.
func parkMeasure(t *testing.T, ctx context.Context, s *MYSQLStore, cardID int, scope string, area int64,
	sheets []string, pieces ...string) {
	t.Helper()
	_, err := s.TechCards().PutTechCardPatternSizeIndex(ctx, entity.PatternSizeIndexWrite{
		TechCardId: cardID, ScopeKey: scope, SheetLineKeys: sheets, SizeTokens: []string{"m"}, ParsedBy: "tester",
	})
	require.NoError(t, err, "size index of %s", scope)
	rows := make([]entity.PieceAreaInput, 0, len(pieces))
	for _, p := range pieces {
		rows = append(rows, entity.PieceAreaInput{PieceLineKey: p, AreaCm2: decimal.NewFromInt(area)})
	}
	_, err = s.TechCards().SaveTechCardPieceAreas(ctx, entity.PieceAreaWrite{
		TechCardId: cardID, ScopeKey: scope, SheetLineKeys: sheets, Rows: rows, ParsedBy: "tester",
	})
	require.NoError(t, err, "areas of %s", scope)
}

// parkSheetFingerprint is the fingerprint of the sheets a scope addresses TODAY, grouped the way the
// server groups them (fabric identity, plus the singleton-interlining legacy purpose bucket).
func parkSheetFingerprint(t *testing.T, ctx context.Context, s *MYSQLStore, cardID int, scope string) string {
	t.Helper()
	card, err := s.TechCards().GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	lines := entity.RollGoodsLinesOfBom(card.BomItems)
	artifacts := make([]entity.FabricArtifact[entity.PatternSheetRef], 0, len(card.Patterns))
	for _, p := range card.Patterns {
		artifacts = append(artifacts, entity.FabricArtifact[entity.PatternSheetRef]{
			FabricPurpose: p.FabricPurpose.String, BomLineKey: p.BomLineKey.String,
			Value: entity.PatternSheetRef{LineKey: p.LineKey, URL: p.URL, Version: p.Version},
		})
	}
	by := entity.PreferredFabricArtifactsByScope(artifacts, lines)
	for _, line := range lines {
		if legacy, ok := entity.LegacyInterliningPurposeScope(line, lines); ok {
			if refs := by[entity.EffectiveFabricScopeKey(line)]; len(refs) > 0 {
				by[legacy] = refs
			}
		}
	}
	return entity.PatternSheetFingerprint(by[scope])
}

// parkState reports whether a scope's stored size index and areas read as current data.
func parkState(t *testing.T, ctx context.Context, s *MYSQLStore, cardID int, scope string) (indexFresh, areasFresh bool) {
	t.Helper()
	areas, err := s.TechCards().GetTechCardPieceAreas(ctx, cardID)
	require.NoError(t, err)
	sc, ok := areas[scope]
	require.True(t, ok, "no areas under %s", scope)
	index, err := s.TechCards().GetTechCardPatternSizeIndex(ctx, cardID)
	require.NoError(t, err)
	row, ok := index[scope]
	require.True(t, ok, "no size index under %s", scope)
	state, _ := entity.PatternSizeIndexStatus(&row, parkSheetFingerprint(t, ctx, s, cardID, scope))
	return state != entity.PatternSizeIndexMissing && state != entity.PatternSizeIndexStale, !sc.Stale
}

func parkRequireFresh(t *testing.T, ctx context.Context, s *MYSQLStore, cardID int, scope string) {
	t.Helper()
	index, areas := parkState(t, ctx, s, cardID, scope)
	require.True(t, index, "size index of %s must read current", scope)
	require.True(t, areas, "areas of %s must read current", scope)
}

func parkRefusedAsDead(t *testing.T, ctx context.Context, s *MYSQLStore, cardID int, scope string, sheets []string) {
	t.Helper()
	_, err := s.TechCards().PutTechCardPatternSizeIndex(ctx, entity.PatternSizeIndexWrite{
		TechCardId: cardID, ScopeKey: scope, SheetLineKeys: sheets,
	})
	var violation *entity.ValidationError
	require.ErrorAs(t, err, &violation, "a parked bucket must not be writable as a live scope")
	require.Equal(t, "scope_not_live", violation.Reason)
}

func parkCard(name string, sizeID int, bom []entity.TechCardBomItem, pieces []string,
	patterns []entity.TechCardSizePattern, aliases []entity.TechCardPieceDxfAlias) *entity.TechCardInsert {
	ps := make([]entity.TechCardPiece, 0, len(pieces))
	for i, key := range pieces {
		ps = append(ps, entity.TechCardPiece{
			LineKey: key, Name: fmt.Sprintf("piece %d", i+1), PiecesPerGarment: 1, Grainline: "lengthwise",
		})
	}
	return &entity.TechCardInsert{
		Name: name, StyleNumber: parkNS(strings.ReplaceAll(name, " ", "-")),
		Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
		Purpose:            entity.TechCardPurposeSellable,
		SeasonCode:         parkNS("SS"),
		SeasonYear:         sql.NullInt32{Int32: 2026, Valid: true},
		MeasurementUnit:    entity.TechCardUnitMm,
		SizeIds:            []int{sizeID},
		BomItems:           bom,
		Pieces:             ps,
		Patterns:           patterns,
		PieceDxfAliasesSet: true,
		PieceDxfAliases:    aliases,
	}
}

func parkSheet(sizeID int, key, file, purpose, line string) entity.TechCardSizePattern {
	return entity.TechCardSizePattern{
		SizeId: sizeID, URL: "https://cdn.example/tech-card-patterns/" + file, Filename: parkNS(file),
		Name: parkNS(strings.TrimSuffix(file, ".dxf")), LineKey: key,
		FabricPurpose: parkNS(purpose), BomLineKey: parkNS(line),
	}
}

func parkAdd(t *testing.T, ctx context.Context, s *MYSQLStore, in *entity.TechCardInsert) (int, func(*entity.TechCardInsert) error) {
	t.Helper()
	cardID, err := s.TechCards().AddTechCard(ctx, in)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", cardID)
	})
	// Side writes (size index, areas, markers) move the card's lock too: save at the stored version.
	return cardID, func(next *entity.TechCardInsert) error {
		var lock int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT lock_version FROM tech_card WHERE id = ?", cardID).Scan(&lock))
		return s.TechCards().UpdateTechCard(ctx, cardID, next, lock)
	}
}

// TestFabricScopeParkReSortAndBack — the owner's case: re-sort the BOM purposes A→B, then back.
//
// The shell moves main → contrast with its sheet: one destination, vacant, so its rows follow (and
// come back the same way). The felt is an INTERLINING line whose rows an older client filed under
// its legacy purpose key «interfacing»; re-sorting the felt to «other» moves no sheet (the felt is
// its own scope throughout), so before this lane the save deleted those rows as a dead bucket.
func TestFabricScopeParkReSortAndBack(t *testing.T) {
	ctx, s, sizeID := sheetBindingReviewStore(t)
	const (
		lineM  = "01FSPRSMAINLINE00000000001"
		lineF  = "01FSPRSFELTLINE00000000002"
		pieceM = "01FSPRSPIECEFRONT000000003"
		pieceF = "01FSPRSPIECECOLLAR00000004"
		sheetM = "01FSPRSSHEETMAIN0000000005"
		sheetF = "01FSPRSSHEETFELT0000000006"
	)
	card := func(shell, felt string) *entity.TechCardInsert {
		return parkCard("Park resort", sizeID,
			[]entity.TechCardBomItem{
				{LineKey: lineM, Section: entity.BomSectionFabric, Name: "shell", Purpose: parkNS(shell)},
				{LineKey: lineF, Section: entity.BomSectionInterlining, Name: "felt", Purpose: parkNS(felt)},
			},
			[]string{pieceM, pieceF},
			[]entity.TechCardSizePattern{
				parkSheet(sizeID, sheetM, "park-shell.dxf", shell, lineM),
				parkSheet(sizeID, sheetF, "park-felt.dxf", "", lineF),
			},
			[]entity.TechCardPieceDxfAlias{
				{FabricPurpose: shell, BomLineKey: lineM, BlockName: "FRONT", PieceLineKey: pieceM},
				{BomLineKey: lineF, BlockName: "COLLAR", PieceLineKey: pieceF},
			})
	}
	cardID, resave := parkAdd(t, ctx, s, card("main", "interfacing"))
	parkMeasure(t, ctx, s, cardID, "main", 111, []string{sheetM}, pieceM)
	parkMeasure(t, ctx, s, cardID, "interfacing", 222, []string{sheetF}, pieceF)
	parkRequireFresh(t, ctx, s, cardID, "main")
	parkRequireFresh(t, ctx, s, cardID, "interfacing")
	require.Equal(t, [2]int{2, 2}, parkTotals(t, ctx, cardID))

	// A → B.
	require.NoError(t, resave(card("contrast", "other")))
	require.Equal(t, [2]int{2, 2}, parkTotals(t, ctx, cardID), "a re-sort must not delete a single sidecar row")
	require.Equal(t, [2]int{1, 1}, parkRows(t, ctx, cardID, "interfacing"),
		"the felt's rows stay parked under the purpose it no longer carries")
	require.Equal(t, [2]int{1, 1}, parkRows(t, ctx, cardID, "contrast"), "one-destination move into a vacant scope rekeys")
	require.Equal(t, [2]int{0, 0}, parkRows(t, ctx, cardID, "main"))
	require.Equal(t, [2]int{0, 0}, parkRows(t, ctx, cardID, "other"), "the felt's new legacy key must not inherit them")
	parkRequireFresh(t, ctx, s, cardID, "contrast")
	// The parked bucket is no slot's data: nothing reads it as current and nothing can write it.
	if _, areasFresh := parkState(t, ctx, s, cardID, "interfacing"); areasFresh {
		t.Fatal("a parked bucket must read stale while no line addresses it")
	}
	parkRefusedAsDead(t, ctx, s, cardID, "interfacing", []string{sheetF})

	// B → A: the same rows, current again.
	require.NoError(t, resave(card("main", "interfacing")))
	require.Equal(t, [2]int{2, 2}, parkTotals(t, ctx, cardID))
	require.Equal(t, [2]int{1, 1}, parkRows(t, ctx, cardID, "main"))
	require.Equal(t, [2]int{1, 1}, parkRows(t, ctx, cardID, "interfacing"))
	require.Equal(t, [2]int{0, 0}, parkRows(t, ctx, cardID, "contrast"))
	parkRequireFresh(t, ctx, s, cardID, "main")
	parkRequireFresh(t, ctx, s, cardID, "interfacing")
}

// TestFabricScopeParkSplitKeepsTheSourceSet — a legacy slot holding two files (card-58 shape, written
// before the one-file rule) is split into two purposes. The two-file measurement has no truthful
// single destination, so it is parked, not discarded. Sorting back into ONE slot is refused by the
// one-file rule (lane 0), and a partial return reads the parked set as stale: it can never pose as
// current data for a different set of files.
func TestFabricScopeParkSplitKeepsTheSourceSet(t *testing.T) {
	ctx, s, sizeID := sheetBindingReviewStore(t)
	const (
		line1  = "01FSPSPSHELLA0000000000001"
		line2  = "01FSPSPSHELLB0000000000002"
		piece  = "01FSPSPPIECEFRONT000000003"
		sheet1 = "01FSPSPSHEETA0000000000004"
		sheet2 = "01FSPSPSHEETB0000000000005"
	)
	card := func(p1, p2 string, sheets ...entity.TechCardSizePattern) *entity.TechCardInsert {
		return parkCard("Park split", sizeID,
			[]entity.TechCardBomItem{
				{LineKey: line1, Section: entity.BomSectionFabric, Name: "shell A", Purpose: parkNS(p1)},
				{LineKey: line2, Section: entity.BomSectionFabric, Name: "shell B", Purpose: parkNS(p2)},
			},
			[]string{piece}, sheets,
			[]entity.TechCardPieceDxfAlias{{FabricPurpose: p1, BomLineKey: line1, BlockName: "FRONT", PieceLineKey: piece}})
	}
	s1 := func(p string) entity.TechCardSizePattern { return parkSheet(sizeID, sheet1, "split-a.dxf", p, line1) }
	s2 := func(p string) entity.TechCardSizePattern { return parkSheet(sizeID, sheet2, "split-b.dxf", p, line2) }
	cardID, resave := parkAdd(t, ctx, s, card("main", "main", s1("main")))
	// The second file arrives behind the rule's back, as on card 58.
	_, err := testDB.ExecContext(ctx, `INSERT INTO tech_card_size_pattern
		(tech_card_id, size_id, url, filename, name, line_key, version, fabric_purpose, bom_line_key)
		VALUES (?, ?, 'https://cdn.example/tech-card-patterns/split-b.dxf', 'split-b.dxf', 'split-b', ?, 1, 'main', ?)`,
		cardID, sizeID, sheet2, line2)
	require.NoError(t, err)
	require.NoError(t, resave(card("main", "main", s1("main"), s2("main"))), "a stored two-file slot stays writable")
	parkMeasure(t, ctx, s, cardID, "main", 150, []string{sheet1, sheet2}, piece)
	parkRequireFresh(t, ctx, s, cardID, "main")

	// Split: each file follows its own line into its own purpose.
	require.NoError(t, resave(card("lining", "contrast", s1("lining"), s2("contrast"))))
	require.Equal(t, [2]int{1, 1}, parkRows(t, ctx, cardID, "main"), "a split parks the source set")
	require.Equal(t, [2]int{0, 0}, parkRows(t, ctx, cardID, "lining"))
	require.Equal(t, [2]int{0, 0}, parkRows(t, ctx, cardID, "contrast"))
	parkRefusedAsDead(t, ctx, s, cardID, "main", []string{sheet1})

	// Back into one slot: two singly-filled slots may not merge (one file per slot).
	err = resave(card("main", "main", s1("main"), s2("main")))
	require.Error(t, err)
	require.Contains(t, err.Error(), `material slot "main" already holds a pattern file`)
	require.Equal(t, [2]int{1, 1}, parkRows(t, ctx, cardID, "main"))

	// Back with one of the two files: «main» is live again, but over a different set of files.
	require.NoError(t, resave(card("main", "main", s1("main"))))
	require.Equal(t, [2]int{1, 1}, parkRows(t, ctx, cardID, "main"))
	index, areas := parkState(t, ctx, s, cardID, "main")
	require.False(t, index, "a parked two-file index must read stale over one file")
	require.False(t, areas, "parked two-file areas must read stale over one file")
}

// TestFabricScopeParkMergeIntoAnOccupiedScope — the shell is sorted into «lining», whose own
// measurement is still stored (its file was dropped earlier). The destination keeps its rows; the
// shell's rows are parked, not deleted, and sorting the shell back makes them current again.
func TestFabricScopeParkMergeIntoAnOccupiedScope(t *testing.T) {
	ctx, s, sizeID := sheetBindingReviewStore(t)
	const (
		lineShell  = "01FSPMGSHELL00000000000001"
		lineLining = "01FSPMGLINING0000000000002"
		pieceFront = "01FSPMGPIECEFRONT000000003"
		pieceBack  = "01FSPMGPIECEBACK0000000004"
		sheetShell = "01FSPMGSHEETSHELL000000005"
		sheetLin   = "01FSPMGSHEETLINING00000006"
	)
	card := func(shell string, withLiningSheet bool) *entity.TechCardInsert {
		sheets := []entity.TechCardSizePattern{parkSheet(sizeID, sheetShell, "merge-shell.dxf", shell, lineShell)}
		if withLiningSheet {
			sheets = append(sheets, parkSheet(sizeID, sheetLin, "merge-lining.dxf", "lining", lineLining))
		}
		return parkCard("Park merge", sizeID,
			[]entity.TechCardBomItem{
				{LineKey: lineShell, Section: entity.BomSectionFabric, Name: "shell", Purpose: parkNS(shell)},
				{LineKey: lineLining, Section: entity.BomSectionLining, Name: "lining", Purpose: parkNS("lining")},
			},
			[]string{pieceFront, pieceBack}, sheets,
			[]entity.TechCardPieceDxfAlias{
				{FabricPurpose: shell, BomLineKey: lineShell, BlockName: "FRONT", PieceLineKey: pieceFront},
				{FabricPurpose: "lining", BomLineKey: lineLining, BlockName: "BACK", PieceLineKey: pieceBack},
			})
	}
	cardID, resave := parkAdd(t, ctx, s, card("main", true))
	parkMeasure(t, ctx, s, cardID, "main", 111, []string{sheetShell}, pieceFront)
	parkMeasure(t, ctx, s, cardID, "lining", 222, []string{sheetLin}, pieceBack)
	require.NoError(t, resave(card("main", false)))
	require.Equal(t, [2]int{1, 1}, parkRows(t, ctx, cardID, "lining"), "dropping a slot's file keeps its rows")

	area := func(scope string) string {
		var v string
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT area_cm2 FROM tech_card_piece_area
			WHERE tech_card_id = ? AND scope_key = ?`, cardID, scope).Scan(&v))
		return v
	}
	require.NoError(t, resave(card("lining", false)))
	require.Equal(t, [2]int{1, 1}, parkRows(t, ctx, cardID, "main"), "the merged source is parked, not deleted")
	require.Equal(t, [2]int{1, 1}, parkRows(t, ctx, cardID, "lining"))
	require.Equal(t, "222.00", area("lining"), "the occupied destination keeps its own measurement")
	parkRefusedAsDead(t, ctx, s, cardID, "main", []string{sheetShell})

	require.NoError(t, resave(card("main", false)))
	require.Equal(t, [2]int{1, 1}, parkRows(t, ctx, cardID, "main"))
	require.Equal(t, "111.00", area("main"))
	require.Equal(t, "222.00", area("lining"))
	parkRequireFresh(t, ctx, s, cardID, "main")
}

// TestFabricScopeParkOrphanBucketSurvivesASave — rows under keys nothing on the card resolves to (a
// purpose no line carries, a line that left the BOM). An unchanged save keeps them; they read stale
// and the side RPCs refuse them, so they can never pass for a live slot's data.
func TestFabricScopeParkOrphanBucketSurvivesASave(t *testing.T) {
	ctx, s, sizeID := sheetBindingReviewStore(t)
	const (
		lineM    = "01FSPORMAINLINE00000000001"
		piece    = "01FSPORPIECEFRONT000000002"
		sheet    = "01FSPORSHEETMAIN0000000003"
		goneLine = "01FSPORGONELINE00000000004"
	)
	in := parkCard("Park orphan", sizeID,
		[]entity.TechCardBomItem{{LineKey: lineM, Section: entity.BomSectionFabric, Name: "shell", Purpose: parkNS("main")}},
		[]string{piece},
		[]entity.TechCardSizePattern{parkSheet(sizeID, sheet, "orphan-shell.dxf", "main", lineM)},
		[]entity.TechCardPieceDxfAlias{{FabricPurpose: "main", BomLineKey: lineM, BlockName: "FRONT", PieceLineKey: piece}})
	cardID, resave := parkAdd(t, ctx, s, in)
	parkMeasure(t, ctx, s, cardID, "main", 111, []string{sheet}, piece)
	fp := strings.Repeat("d", 64)
	for _, scope := range []string{"mesh", goneLine} {
		_, err := testDB.ExecContext(ctx, `INSERT INTO tech_card_pattern_size_index
			(tech_card_id, scope_key, sheet_fingerprint, size_tokens, parsed_by) VALUES (?, ?, ?, '["m"]', 'test')`,
			cardID, scope, fp)
		require.NoError(t, err)
		_, err = testDB.ExecContext(ctx, `INSERT INTO tech_card_piece_area
			(tech_card_id, scope_key, piece_line_key, area_cm2, sheet_fingerprint, parsed_by) VALUES (?, ?, ?, 10, ?, 'test')`,
			cardID, scope, piece, fp)
		require.NoError(t, err)
	}

	require.NoError(t, resave(in))
	require.Equal(t, [2]int{3, 3}, parkTotals(t, ctx, cardID), "a card save must not delete orphan buckets")
	for _, scope := range []string{"mesh", goneLine} {
		require.Equal(t, [2]int{1, 1}, parkRows(t, ctx, cardID, scope))
		if _, fresh := parkState(t, ctx, s, cardID, scope); fresh {
			t.Fatalf("orphan bucket %s must read stale", scope)
		}
		parkRefusedAsDead(t, ctx, s, cardID, scope, []string{sheet})
	}
	parkRequireFresh(t, ctx, s, cardID, "main")
}

// bindingSlotCard: a shell plus a bias-binding roll-goods line, each with its own file and piece.
func bindingSlotCard(sizeID int, bindSheet entity.TechCardSizePattern, extra ...entity.TechCardSizePattern) *entity.TechCardInsert {
	sheets := []entity.TechCardSizePattern{parkSheet(sizeID, bindShellSheet, "bind-shell.dxf", "main", bindShellLine)}
	if bindSheet.LineKey != "" {
		sheets = append(sheets, bindSheet)
	}
	sheets = append(sheets, extra...)
	in := parkCard("Park binding", sizeID,
		[]entity.TechCardBomItem{
			{LineKey: bindShellLine, Section: entity.BomSectionFabric, Name: "shell", Purpose: parkNS("main"),
				FabricDirection: parkNS("any")},
			{LineKey: bindBiasLine, Section: entity.BomSectionLining, Name: "bias", Purpose: parkNS("binding"),
				FabricDirection: parkNS("any")},
		},
		[]string{bindPieceFront, bindPieceNeck}, sheets,
		[]entity.TechCardPieceDxfAlias{
			{FabricPurpose: "main", BomLineKey: bindShellLine, BlockName: "FRONT", PieceLineKey: bindPieceFront},
			{FabricPurpose: "binding", BomLineKey: bindBiasLine, BlockName: "NECKBIND", PieceLineKey: bindPieceNeck},
		})
	return in
}

const (
	bindShellLine  = "01FSPBNSHELLLINE0000000001"
	bindBiasLine   = "01FSPBNBIASLINE00000000002"
	bindPieceFront = "01FSPBNPIECEFRONT000000003"
	bindPieceNeck  = "01FSPBNPIECENECK0000000004"
	bindShellSheet = "01FSPBNSHEETSHELL000000005"
	bindBiasSheet  = "01FSPBNSHEETBIAS0000000006"
	bindBiasSheet2 = "01FSPBNSHEETBIAS2000000007"
)

func bindingMarker(t *testing.T, sizeID int) entity.TechCardMarkerInsert {
	d := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }
	m := entity.TechCardMarkerInsert{
		Name: "bias", Source: entity.MarkerSourceAuto, BomLineKey: bindBiasLine,
		FabricWidthCm: d("140"), GapCm: d("0.5"), EdgeMarginCm: d("1"), UsedLengthCm: d("40"),
		PlacedCount: 1, TotalCount: 1,
		Layout: markerLayoutV1, LayoutFacts: markerLayoutFacts(t, markerLayoutV1),
	}
	markerSizing(&m, sizeID, 1)
	return m
}

// TestBindingSlotIsAnOrdinarySlot — owner decision §6.2: a sheet bound to the binding line, or only
// to the binding purpose, is an ordinary slot — it uploads and saves, holds one file, takes a size
// index and measured areas that read current, and carries a marker.
func TestBindingSlotIsAnOrdinarySlot(t *testing.T) {
	ctx, s, sizeID := sheetBindingReviewStore(t)
	byLine := parkSheet(sizeID, bindBiasSheet, "bias.dxf", "binding", bindBiasLine)
	cardID, resave := parkAdd(t, ctx, s, bindingSlotCard(sizeID, byLine))
	parkMeasure(t, ctx, s, cardID, "binding", 40, []string{bindBiasSheet}, bindPieceNeck)
	parkRequireFresh(t, ctx, s, cardID, "binding")
	_, err := s.TechCards().SaveMarker(ctx, cardID, 0, bindingMarker(t, sizeID), "tester")
	require.NoError(t, err, "a marker on the binding line saves like on any cloth")

	// Purpose-only binding: the same slot, still current.
	byPurpose := parkSheet(sizeID, bindBiasSheet, "bias.dxf", "binding", "")
	require.NoError(t, resave(bindingSlotCard(sizeID, byPurpose)))
	require.Equal(t, [2]int{1, 1}, parkRows(t, ctx, cardID, "binding"))
	parkRequireFresh(t, ctx, s, cardID, "binding")

	// One file per slot, named by its purpose word, never by a key.
	second := parkSheet(sizeID, bindBiasSheet2, "bias-2.dxf", "binding", bindBiasLine)
	err = resave(bindingSlotCard(sizeID, byPurpose, second))
	require.Error(t, err)
	require.Contains(t, err.Error(), `material slot "binding" already holds a pattern file`)
	for _, key := range []string{bindBiasLine, bindBiasSheet, bindBiasSheet2} {
		require.NotContains(t, err.Error(), key)
	}
}

// TestDroppingEverySheetOfASlotKeepsItsData — owner decision §6.3: removing the last file of a slot
// keeps the BOM line, its recipe row, its marker, its aliases, its areas and its size index. The
// stored measurement then reads stale (no file backs it) until a file is uploaded and re-measured.
func TestDroppingEverySheetOfASlotKeepsItsData(t *testing.T) {
	ctx, s, sizeID := sheetBindingReviewStore(t)
	T := s.TechCards()
	cardID, resave := parkAdd(t, ctx, s,
		bindingSlotCard(sizeID, parkSheet(sizeID, bindBiasSheet, "bias.dxf", "binding", bindBiasLine)))
	parkMeasure(t, ctx, s, cardID, "binding", 40, []string{bindBiasSheet}, bindPieceNeck)
	_, err := T.SaveMarker(ctx, cardID, 0, bindingMarker(t, sizeID), "tester")
	require.NoError(t, err)

	// A colourway whose recipe takes the bias line.
	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
	})
	require.NoError(t, err)
	res, err := testDB.ExecContext(ctx, `INSERT INTO product
		(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id)
		VALUES (?, 'c', 'BLK', '#000000', 'US', ?, ?)`, fmt.Sprintf("FSPB-CW-%d", cardID), mediaID, cardID)
	require.NoError(t, err)
	cw, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", cw) })
	_, err = testDB.ExecContext(ctx, `INSERT INTO tech_card_colorway_usage (colorway_id, bom_item_id, consumption, display_order)
		SELECT ?, id, 0.3, 0 FROM tech_card_bom_item WHERE tech_card_id = ? AND line_key = ?`, cw, cardID, bindBiasLine)
	require.NoError(t, err)
	usages := func() int {
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM tech_card_colorway_usage u
			JOIN tech_card_bom_item b ON b.id = u.bom_item_id
			WHERE b.tech_card_id = ? AND b.line_key = ?`, cardID, bindBiasLine).Scan(&n))
		return n
	}
	require.Equal(t, 1, usages())

	require.NoError(t, resave(bindingSlotCard(sizeID, entity.TechCardSizePattern{})), "dropping the slot's only file saves")
	got, err := T.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	require.Len(t, got.Patterns, 1, "only the shell's file is left")
	lineKept := false
	for _, b := range got.BomItems {
		lineKept = lineKept || b.LineKey == bindBiasLine
	}
	require.True(t, lineKept, "the BOM line stays")
	require.Equal(t, 1, usages(), "the recipe row stays")
	require.Len(t, got.Markers, 1, "the marker stays")
	require.Equal(t, bindBiasLine, got.Markers[0].BomLineKey.String, "and stays bound to its line")
	aliasKept := false
	for _, a := range got.PieceDxfAliases {
		aliasKept = aliasKept || a.BlockName == "NECKBIND"
	}
	require.True(t, aliasKept, "the block alias stays")
	require.Equal(t, [2]int{1, 1}, parkRows(t, ctx, cardID, "binding"), "areas and size index stay")
	index, areas := parkState(t, ctx, s, cardID, "binding")
	require.False(t, index, "a sheetless slot's index reads stale")
	require.False(t, areas, "a sheetless slot's areas read stale")
}

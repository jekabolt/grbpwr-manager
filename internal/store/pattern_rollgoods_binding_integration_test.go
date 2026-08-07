package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestPatternRollGoodsBinding pins the roll-goods amendment: a выкройка and a cut-piece alias bind
// to the same four BOM families a marker can lay out — fabric, lining, interlining, insulation —
// not to fabric alone.
//
// Before this, the lining's DXF could not be bound at all: the slot filter in insertTechCardPatterns
// and upsertTechCardPieceDxfAliases asked for section='fabric', so the lining slot read as "not a
// slot of this card" and the write failed a field violation — from INSIDE UpdateTechCard, taking
// the WHOLE card save down with it rather than declining one field. A garment whose lining is cut
// from a pattern (i.e. every lined garment) therefore had no way to carry its lining sheet.
//
// The negative side still has to hold, and it is the reason this is not simply "accept any slot":
// thread, hardware and trim are counted, not laid out, so a sheet bound there would be a marker
// nobody can compute. TestPatternLineKeys already guards trim for patterns; this covers aliases.
func TestPatternRollGoodsBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()

	var szA int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&szA))

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	const (
		slotFabric      = "01RGFABRIC00000000000000A1"
		slotLining      = "01RGLINING00000000000000A2"
		slotInterlining = "01RGINTERLINING000000000A3"
		slotInsulation  = "01RGINSULATION0000000000A4"
		slotThread      = "01RGTHREAD00000000000000A5"
		pcFront         = "01RGPIECEFRONT0000000000P1"
		sheetFabric     = "01RGSHEETFABRIC000000000S1"
		sheetLining     = "01RGSHEETLINING000000000S2"
		sheetIntr       = "01RGSHEETINTERLINING0000S3"
		sheetInsl       = "01RGSHEETINSULATION00000S4"
		urlBase         = "https://cdn.example/base/tech-card-patterns/2026/august/"
	)
	bom := []entity.TechCardBomItem{
		{LineKey: slotFabric, Section: entity.BomSectionFabric, Name: "Основная"},
		{LineKey: slotLining, Section: entity.BomSectionLining, Name: "Подкладка"},
		{LineKey: slotInterlining, Section: entity.BomSectionInterlining, Name: "Бортовка"},
		{LineKey: slotInsulation, Section: entity.BomSectionInsulation, Name: "Утеплитель"},
		{LineKey: slotThread, Section: entity.BomSectionThread, Name: "Нитки"},
	}
	card := func(patterns []entity.TechCardSizePattern, aliases []entity.TechCardPieceDxfAlias) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "Roll Goods", StyleNumber: ns("RG-1"),
			Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			SizeIds:         []int{szA},
			BomItems:        bom,
			Pieces: []entity.TechCardPiece{
				{LineKey: pcFront, Name: "перед", PiecesPerGarment: 1, Grainline: "lengthwise"},
			},
			Patterns:           patterns,
			PieceDxfAliases:    aliases,
			PieceDxfAliasesSet: aliases != nil,
		}
	}

	tcID, err := T.AddTechCard(ctx, card(nil, nil))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	lock := 0
	resave := func(patterns []entity.TechCardSizePattern, aliases []entity.TechCardPieceDxfAlias) error {
		err := T.UpdateTechCard(ctx, tcID, card(patterns, aliases), lock)
		if err == nil {
			lock++
		}
		return err
	}
	readCard := func() *entity.TechCard {
		c, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		return c
	}

	// All four roll-goods families take a sheet in one save.
	sheets := []entity.TechCardSizePattern{
		{SizeId: szA, URL: urlBase + "rg-fabric.dxf", LineKey: sheetFabric, BomLineKey: ns(slotFabric)},
		{SizeId: szA, URL: urlBase + "rg-lining.dxf", LineKey: sheetLining, BomLineKey: ns(slotLining)},
		{SizeId: szA, URL: urlBase + "rg-interlining.dxf", LineKey: sheetIntr, BomLineKey: ns(slotInterlining)},
		{SizeId: szA, URL: urlBase + "rg-insulation.dxf", LineKey: sheetInsl, BomLineKey: ns(slotInsulation)},
	}
	require.NoError(t, resave(sheets, nil), "lining/interlining/insulation must accept a pattern sheet")

	boundTo := map[string]string{}
	for _, p := range readCard().Patterns {
		if p.BomLineKey.Valid {
			boundTo[p.LineKey] = p.BomLineKey.String
		}
	}
	require.Equal(t, map[string]string{
		sheetFabric: slotFabric,
		sheetLining: slotLining,
		sheetIntr:   slotInterlining,
		sheetInsl:   slotInsulation,
	}, boundTo)

	// The same widening for aliases. The block name repeats across slots on purpose — a grader
	// names the lining's front piece exactly what it names the shell's, and slot scope is what
	// keeps those two from colliding.
	require.NoError(t, resave(sheets, []entity.TechCardPieceDxfAlias{
		{BomLineKey: slotFabric, BlockName: "FP_1", PieceLineKey: pcFront},
		{BomLineKey: slotLining, BlockName: "FP_1", PieceLineKey: pcFront},
		{BomLineKey: slotInterlining, BlockName: "FP_1", PieceLineKey: pcFront},
		{BomLineKey: slotInsulation, BlockName: "FP_1", PieceLineKey: pcFront},
	}), "lining/interlining/insulation must accept a cut-piece alias")
	aliasSlots := map[string]bool{}
	for _, a := range readCard().PieceDxfAliases {
		aliasSlots[a.BomLineKey] = true
	}
	require.Equal(t, map[string]bool{
		slotFabric: true, slotLining: true, slotInterlining: true, slotInsulation: true,
	}, aliasSlots)

	// A counted section is still refused — a thread line has no width to lay anything out on.
	require.Error(t, resave(sheets, []entity.TechCardPieceDxfAlias{
		{BomLineKey: slotThread, BlockName: "FP_1", PieceLineKey: pcFront},
	}), "thread-section slot must not take an alias")

	// Rollback, pinned so it can actually fail. The payload carries a NEW alias pair on a GOOD slot
	// FOLLOWED BY the bad one: the good pair inserts, the bad one violates, and only a rollback can
	// leave the count at four. Asserting the count after a payload whose every row is either absent
	// or already stored proves nothing — the write never reaches an INSERT, so it survives with or
	// without a transaction.
	require.Error(t, resave(sheets, []entity.TechCardPieceDxfAlias{
		{BomLineKey: slotFabric, BlockName: "FP_1", PieceLineKey: pcFront},
		{BomLineKey: slotLining, BlockName: "FP_1", PieceLineKey: pcFront},
		{BomLineKey: slotInterlining, BlockName: "FP_1", PieceLineKey: pcFront},
		{BomLineKey: slotInsulation, BlockName: "FP_1", PieceLineKey: pcFront},
		{BomLineKey: slotLining, BlockName: "FP_2_НОВЫЙ", PieceLineKey: pcFront},
		{BomLineKey: slotThread, BlockName: "FP_1", PieceLineKey: pcFront},
	}), "a bad pair anywhere in the set must fail the whole set")
	require.Len(t, readCard().PieceDxfAliases, 4, "the new pair must have rolled back with the bad one")

	// Same shape on the pattern side: the failing payload also REBINDS a good sheet, so a partial
	// write would be visible as a moved binding rather than only as a missing row.
	require.Error(t, resave(append(append([]entity.TechCardSizePattern{},
		entity.TechCardSizePattern{SizeId: szA, URL: urlBase + "rg-fabric.dxf", LineKey: sheetFabric, BomLineKey: ns(slotLining)},
		sheets[1], sheets[2], sheets[3]),
		entity.TechCardSizePattern{SizeId: szA, URL: urlBase + "rg-thread.dxf", BomLineKey: ns(slotThread)}), nil),
		"thread-section slot must not take a pattern sheet")
	after := map[string]string{}
	for _, p := range readCard().Patterns {
		if p.BomLineKey.Valid {
			after[p.LineKey] = p.BomLineKey.String
		}
	}
	require.Equal(t, slotFabric, after[sheetFabric], "the rebind must have rolled back with the bad row")
}

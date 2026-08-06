package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestFabricDirectionGapsReport covers the кампания Д1 worklist (Ф1.8) against a real MySQL. A bind
// test proves the query PARSES; only this proves it RUNS — the family filter has to be qualified or
// MySQL calls `section` ambiguous against the material join, and the boolean/count projections have
// to scan. Then the report's own promises: only roll goods, the catalogue-resolved name, the unset
// test, the per-line blocked-marker count, and the released/obsolete scope with its always-present
// price.
//
// SAFE ONLY against a local container DSN: this suite's TestMain drops every table on cleanup (see
// mysql_test.go / project memory). It refuses to run against anything else.
func TestFabricDirectionGapsReport(t *testing.T) {
	if testCfg == nil {
		t.Skip("no test database configured")
	}
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

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	newCard := func(number, name string, state entity.TechCardApprovalState, items ...entity.TechCardBomItem) int {
		id, err := T.AddTechCard(ctx, &entity.TechCardInsert{
			Name: name, Stage: entity.TechCardStageProto, StyleNumber: ns(number),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: state,
			SizeIds:  []int{szA},
			BomItems: items,
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", id)
		})
		return id
	}
	line := func(key, name string, section entity.TechCardBomSection, direction string) entity.TechCardBomItem {
		b := entity.TechCardBomItem{LineKey: key, Section: section, Name: name}
		if direction != "" {
			b.FabricDirection = ns(direction)
		}
		return b
	}

	// A card whose fabric is unset, whose lining is answered, and which also carries a thread — a
	// family that has no direction to have and must never appear.
	gapID := newCard("FDR-GAP", "Gap Style", entity.TechCardApprovalDraft,
		line("01FDRFABRIC000000000000001", "Основная", entity.BomSectionFabric, ""),
		line("01FDRLINING000000000000001", "Подкладка", entity.BomSectionLining, "one_way"),
		line("01FDRINTERLIN0000000000001", "Дублерин", entity.BomSectionInterlining, ""),
		line("01FDRTHREAD000000000000001", "Нитки", entity.BomSectionThread, ""),
	)
	// A card with every cloth line answered: absent from the report entirely, not present-and-empty.
	doneID := newCard("FDR-DONE", "Done Style", entity.TechCardApprovalDraft,
		line("01FDRDONEFABRIC00000000001", "Основная", entity.BomSectionFabric, "any"),
	)
	// The two default exclusions, each with one unset cloth line.
	releasedID := newCard("FDR-REL", "Released Style", entity.TechCardApprovalDraft,
		line("01FDRRELFABRIC000000000001", "Основная", entity.BomSectionFabric, ""))
	_, err = testDB.ExecContext(ctx, "UPDATE tech_card SET approval_state = 'released' WHERE id = ?", releasedID)
	require.NoError(t, err)
	obsoleteID := newCard("FDR-OBS", "Obsolete Style", entity.TechCardApprovalDraft,
		line("01FDROBSFABRIC000000000001", "Основная", entity.BomSectionFabric, ""))
	_, err = testDB.ExecContext(ctx, "UPDATE tech_card SET approval_state = 'obsolete' WHERE id = ?", obsoleteID)
	require.NoError(t, err)

	// A раскладка bound to the unset fabric line of the gap card: the provably-refused tier.
	d := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }
	_, err = T.SaveMarker(ctx, gapID, 0, entity.TechCardMarkerInsert{
		SizeId: szA, Name: "M · основная", Source: entity.MarkerSourceAuto,
		BomLineKey:    "01FDRFABRIC000000000000001",
		FabricWidthCm: d("140"), GapCm: d("0.5"), EdgeMarginCm: d("1"),
		Sets: 1, UsedLengthCm: d("100"), PlacedCount: 2, TotalCount: 2,
		Layout: `{"schemaVersion":1,"pieces":[],"placements":[]}`,
	}, "tester")
	require.NoError(t, err)

	byID := func(cards []entity.FabricDirectionGapCard, id int) *entity.FabricDirectionGapCard {
		for i := range cards {
			if cards[i].TechCardID == id {
				return &cards[i]
			}
		}
		return nil
	}

	// --- the card-scoped read: only roll goods, only unset, name off the BOM tab ---
	scoped, err := T.ListFabricDirectionGaps(ctx, gapID)
	require.NoError(t, err)
	require.Len(t, scoped, 1, "tech_card_id must scope the report to one card")
	gap := scoped[0]
	require.Equal(t, "FDR-GAP", gap.StyleNumber)
	require.Equal(t, "Gap Style", gap.Name)
	require.Equal(t, string(entity.TechCardApprovalDraft), gap.ApprovalState)
	require.Equal(t, 1, gap.LinkedMarkerCount)
	require.False(t, gap.HasPatterns)
	require.Len(t, gap.Lines, 2, "the answered lining and the thread must both be out")
	require.Equal(t, "Основная", gap.Lines[0].Name)
	require.Equal(t, string(entity.BomSectionFabric), gap.Lines[0].Section)
	require.Equal(t, 1, gap.Lines[0].BlockedMarkerCount, "the marker bound to this line is refused")
	require.Equal(t, "Дублерин", gap.Lines[1].Name)
	require.Equal(t, string(entity.BomSectionInterlining), gap.Lines[1].Section)
	require.Zero(t, gap.Lines[1].BlockedMarkerCount)
	require.Equal(t, 1, gap.BlockedMarkerCount())

	// --- a card with nothing outstanding is absent, not empty ---
	all, err := T.ListFabricDirectionGaps(ctx, 0)
	require.NoError(t, err)
	require.Nil(t, byID(all, doneID), "a fully answered card must not appear at all")
	require.NotNil(t, byID(all, releasedID), "the store returns released cards; the scope decision is the report's")
	require.NotNil(t, byID(all, obsoleteID))

	// --- the scope decision, on the store's real output ---
	worklist := entity.BuildFabricDirectionGapReport(all, false)
	require.Nil(t, byID(worklist.Cards, releasedID), "released cards refuse every marker save; out by default")
	require.Nil(t, byID(worklist.Cards, obsoleteID))
	require.NotNil(t, byID(worklist.Cards, gapID))
	require.GreaterOrEqual(t, worklist.ExcludedCards, 2)
	require.GreaterOrEqual(t, worklist.ExcludedLines, 2, "what was hidden is always priced")
	require.Equal(t, gapID, worklist.Cards[0].TechCardID, "the card with a refused раскладка sorts first")

	full := entity.BuildFabricDirectionGapReport(all, true)
	require.NotNil(t, byID(full.Cards, releasedID), "include_inactive folds the hidden rows back in")
	require.NotNil(t, byID(full.Cards, obsoleteID))
	require.Empty(t, full.Excluded)
	require.Equal(t, worklist.TotalLines+worklist.ExcludedLines, full.TotalLines)
	require.False(t, byID(full.Cards, releasedID).MarkerSavePossible())

	// --- filling the field in removes the row: this is what «campaign finished» looks like ---
	_, err = testDB.ExecContext(ctx,
		"UPDATE tech_card_bom_item SET fabric_direction = 'two_way' WHERE tech_card_id = ? AND line_key = ?",
		gapID, "01FDRFABRIC000000000000001")
	require.NoError(t, err)
	after, err := T.ListFabricDirectionGaps(ctx, gapID)
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.Len(t, after[0].Lines, 1, "the answered line drops out")
	require.Equal(t, "Дублерин", after[0].Lines[0].Name)
	require.Zero(t, after[0].BlockedMarkerCount(), "and with it the urgency tier")
}

// The line NAME must come off the catalogue the way the BOM tab resolves it: a material-linked line
// legitimately carries an empty name of its own, and reading the stored column alone would print a
// ULID at an operator looking for the fabric they know by name.
func TestFabricDirectionGapsResolvesCatalogueName(t *testing.T) {
	if testCfg == nil {
		t.Skip("no test database configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()

	var szA int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&szA))

	res, err := testDB.ExecContext(ctx,
		`INSERT INTO material (name, section, unit) VALUES ('ВЕЛЬВЕТ ИЗ КАТАЛОГА', 'fabric', 'm')`)
	require.NoError(t, err)
	matID, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM material WHERE id = ?", matID) })

	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Catalogue Style", Stage: entity.TechCardStageProto,
		StyleNumber:     sql.NullString{String: "FDR-CAT", Valid: true},
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		SizeIds: []int{szA},
		BomItems: []entity.TechCardBomItem{{
			LineKey: "01FDRCATFABRIC000000000001", Section: entity.BomSectionFabric,
			Name:       "",
			MaterialId: sql.NullInt64{Int64: matID, Valid: true},
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	cards, err := T.ListFabricDirectionGaps(ctx, tcID)
	require.NoError(t, err)
	require.Len(t, cards, 1)
	require.Len(t, cards[0].Lines, 1)
	require.Equal(t, "ВЕЛЬВЕТ ИЗ КАТАЛОГА", cards[0].Lines[0].Name)
}

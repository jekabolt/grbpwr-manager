package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// РАСКРОЙНЫЕ МАРКЕРЫ ПРОГОНА (Ф4, migration 0282). ListRunMarkers is the only read that returns them
// at all — a run's раскладки are hidden from the card's list, so without it the lay editor's picker
// would have nothing to offer and a section could reference a row no client can see.
//
// This test exists because the two things that can break here are invisible to a unit test: the
// query has to PARSE against the real schema (m.run_id arrived in 0282, and the summary column list
// is shared with the card read and the single-marker read), and the filter has to actually
// discriminate — a раскладка of another run, and the CARD's own раскладка, must not appear.
func TestListRunMarkers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	T := s.TechCards()
	PR := s.ProductionRuns()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	var szA int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&szA))

	fabric := entity.TechCardBomItem{
		LineKey: "01RUNMARKFABRIC0000000MAIN", Section: entity.BomSectionFabric, Name: "Основная",
		FabricDirection: ns("any"),
	}
	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Run Marker Style", Stage: entity.TechCardStageProto, StyleNumber: ns("RUNMARK-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		SizeIds:         []int{szA},
		BomItems:        []entity.TechCardBomItem{fabric},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	var slot int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM tech_card_bom_item WHERE tech_card_id = ? AND line_key = ?", tcID, fabric.LineKey).Scan(&slot))

	newRun := func() int {
		id, err := PR.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: tcID, Status: entity.ProductionRunInProgress,
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", id)
		})
		return id
	}
	runA, runB := newRun(), newRun()

	// Inserted directly, on purpose: this probe is about the READ, so it builds its rows without
	// going through the write path's own validation. SaveMarker does persist run_id (Ф4 marker step
	// — see TestMarkerRunOwnership for the write side); a fixture that used it here would make a
	// failure of either path fail both tests.
	newMarker := func(name string, runID int) int {
		var runVal any
		if runID > 0 {
			runVal = runID
		}
		res, err := testDB.ExecContext(ctx, `INSERT INTO tech_card_marker
			(tech_card_id, run_id, bom_item_id, size_id, name, source, fabric_width_cm,
			 used_length_cm, total_units, placed_count, total_count, layout)
			VALUES (?, ?, ?, NULL, ?, 'auto', 140.00, 900.00, 2, 4, 4, '{}')`,
			tcID, runVal, slot, name)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return int(id)
	}
	mineA := newMarker("основная 40-42", runA)
	mineB := newMarker("основная добор", runA)
	otherRun := newMarker("чужого прогона", runB)
	cardMarker := newMarker("норма карточки", 0)

	got, err := T.ListRunMarkers(ctx, runA)
	require.NoError(t, err)
	ids := make([]int, 0, len(got))
	for _, m := range got {
		ids = append(ids, m.Id)
		require.Equal(t, int64(runA), m.RunId.Int64, "the read has to carry ownership, not merely filter on it")
		require.True(t, m.RunId.Valid)
	}
	require.ElementsMatch(t, []int{mineA, mineB}, ids)
	require.NotContains(t, ids, otherRun, "another run's раскладка is not this run's to lay")
	require.NotContains(t, ids, cardMarker,
		"a КАРТОЧНЫЙ marker can never be a section (Р2), so the picker must not offer one")

	// And the card's own list is the complement of this one — the card read still answers, and the
	// summary column list it shares with this query still binds.
	card, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.NotNil(t, card)

	empty, err := T.ListRunMarkers(ctx, runB+10_000)
	require.NoError(t, err)
	require.Empty(t, empty, "a run with no раскладки answers an empty list, not an error")
}

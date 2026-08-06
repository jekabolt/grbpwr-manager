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

// TestMarkerFabricDirectionGuard covers Ф1.5/Ф1.6 against the real schema: the direction lives on
// tech_card_bom_item (0073) and the scope a marker falls into is resolved through назначение (0267),
// so the two facts this guard runs on are exactly the two the database owns. The pure rule is
// unit-tested in entity; what is asserted here is that the right rows reach it.
//
// The case that matters most is the LEGACY one: a stored marker carrying a rotation outside today's
// policy must keep saving. Everything else is a refusal, and a refusal is easy to get right; a
// silent retro-invalidation of every measurement on file is what this test exists to prevent.
func TestMarkerFabricDirectionGuard(t *testing.T) {
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
		lineUnknown = "01FDIRUNKNOWN00000000000D1" // направление не задано — the state of almost every line
		lineOneWay  = "01FDIRONEWAY000000000000D2"
		lineTwoWay  = "01FDIRTWOWAY000000000000D3"
		lineMainAny = "01FDIRMAINANY00000000000D4" // назначение main, article 1 — any
		lineMainNap = "01FDIRMAINNAP00000000000D5" // назначение main, article 2 — one_way
	)
	bom := []entity.TechCardBomItem{
		{LineKey: lineUnknown, Section: entity.BomSectionFabric, Name: "Твил без направления"},
		{LineKey: lineOneWay, Section: entity.BomSectionFabric, Name: "Вельвет", FabricDirection: ns("one_way")},
		{LineKey: lineTwoWay, Section: entity.BomSectionFabric, Name: "Джерси", FabricDirection: ns("two_way")},
		{LineKey: lineMainAny, Section: entity.BomSectionFabric, Name: "Основная гладкая",
			Purpose: ns("main"), FabricDirection: ns("any")},
		{LineKey: lineMainNap, Section: entity.BomSectionFabric, Name: "Основная ворсовая",
			Purpose: ns("main"), FabricDirection: ns("one_way")},
	}
	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Direction Style", Stage: entity.TechCardStageProto, StyleNumber: ns("FDIR-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		SizeIds:  []int{szA},
		BomItems: bom,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	d := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }
	// layout blobs and their distilled facts travel together, exactly as the API layer produces them.
	legacy90 := `{"schemaVersion":1,"pieces":[],"placements":[{"pieceId":1,"rotDeg":90}]}`
	v3half := `{"schemaVersion":3,"pieces":[],"placements":[{"pieceId":1,"rotDeg":180}]}`
	v3flip := `{"schemaVersion":3,"pieces":[],"placements":[{"pieceId":1,"flipped":true}]}`
	v3clean := `{"schemaVersion":3,"pieces":[],"placements":[{"pieceId":1}]}`
	ins := func(name, lineKey, layout string, facts entity.MarkerLayoutFacts) entity.TechCardMarkerInsert {
		return entity.TechCardMarkerInsert{
			SizeId: szA, Name: name, Source: entity.MarkerSourceAuto,
			BomLineKey:    lineKey,
			FabricWidthCm: d("140"), GapCm: d("0.5"), EdgeMarginCm: d("1"),
			// The legacy trap in its original form: the editor saved a cross-grain rotation even
			// though cross-grain was NOT permitted. That combination is on file and must survive.
			AllowCrossGrain: false,
			Sets:            1, UsedLengthCm: d("120"),
			PlacedCount: 1, TotalCount: 1,
			Layout: layout, LayoutFacts: facts,
		}
	}

	t.Run("unknown direction blocks the save", func(t *testing.T) {
		var ve *entity.ValidationError
		_, err := T.SaveMarker(ctx, tcID, 0,
			ins("без направления", lineUnknown, v3clean, entity.MarkerLayoutFacts{SchemaVersion: 3}), "tester")
		require.ErrorAs(t, err, &ve)
		require.Equal(t, "bom_items.fabric_direction", ve.Field)
		require.Contains(t, ve.Message, "Твил без направления", "the refusal must name the row to fix")
	})

	t.Run("an unlinked marker on the same card still saves", func(t *testing.T) {
		// No bom_line_key, no cloth to ask about. This was legal before Ф1 and stays legal — the
		// geometry is just as valid without an attribution.
		id, err := T.SaveMarker(ctx, tcID, 0,
			ins("без привязки", "", v3half, entity.MarkerLayoutFacts{SchemaVersion: 3, HasHalfTurn: true}), "tester")
		require.NoError(t, err)
		require.Positive(t, id)
	})

	t.Run("legacy blob keeps saving on one_way cloth", func(t *testing.T) {
		id, err := T.SaveMarker(ctx, tcID, 0,
			ins("легаси 90°", lineOneWay, legacy90, entity.MarkerLayoutFacts{SchemaVersion: 1}), "tester")
		require.NoError(t, err)
		// And so does a legacy blob that carries the very rotations v3 forbids: it predates the
		// policy, and re-judging it would invalidate a measurement nobody can re-take without
		// re-nesting.
		_, err = T.SaveMarker(ctx, tcID, 0, ins("легаси 180°", lineOneWay,
			`{"schemaVersion":2,"pieces":[],"placements":[{"pieceId":1,"rotDeg":180}]}`,
			entity.MarkerLayoutFacts{SchemaVersion: 2, HasHalfTurn: true}), "tester")
		require.NoError(t, err)
		// Re-saving the legacy marker in place is the operator's real action (Ф5 adjustment) and
		// must not start failing either.
		_, err = T.SaveMarker(ctx, tcID, id,
			ins("легаси 90°", lineOneWay, legacy90, entity.MarkerLayoutFacts{SchemaVersion: 1}), "editor")
		require.NoError(t, err)
	})

	t.Run("one_way refuses a new blob turned 180°", func(t *testing.T) {
		var ve *entity.ValidationError
		_, err := T.SaveMarker(ctx, tcID, 0, ins("ворс 180°", lineOneWay, v3half,
			entity.MarkerLayoutFacts{SchemaVersion: 3, HasHalfTurn: true}), "tester")
		require.ErrorAs(t, err, &ve)
		require.Equal(t, "layout.placements", ve.Field)
	})

	t.Run("one_way refuses a new blob with a mirrored placement", func(t *testing.T) {
		var ve *entity.ValidationError
		_, err := T.SaveMarker(ctx, tcID, 0, ins("ворс зеркало", lineOneWay, v3flip,
			entity.MarkerLayoutFacts{SchemaVersion: 3, HasFlip: true}), "tester")
		require.ErrorAs(t, err, &ve)
		require.Equal(t, "layout.placements", ve.Field)
	})

	t.Run("two_way accepts the half-turn", func(t *testing.T) {
		_, err := T.SaveMarker(ctx, tcID, 0, ins("джерси 180°", lineTwoWay, v3half,
			entity.MarkerLayoutFacts{SchemaVersion: 3, HasHalfTurn: true}), "tester")
		require.NoError(t, err)
	})

	// СТРОГОЕ ПОБЕЖДАЕТ. The marker names the smooth article, but its назначение also owns a
	// ворсовый one — and the same geometry gets cut on whichever the colourway pins.
	t.Run("strictest line of the назначение wins", func(t *testing.T) {
		var ve *entity.ValidationError
		_, err := T.SaveMarker(ctx, tcID, 0, ins("main 180°", lineMainAny, v3half,
			entity.MarkerLayoutFacts{SchemaVersion: 3, HasHalfTurn: true}), "tester")
		require.ErrorAs(t, err, &ve)
		require.Equal(t, "layout.placements", ve.Field)
		// Without the forbidden placements the same binding saves — the scope restricts the
		// GEOMETRY, it does not blacklist the line.
		_, err = T.SaveMarker(ctx, tcID, 0, ins("main прямой", lineMainAny, v3clean,
			entity.MarkerLayoutFacts{SchemaVersion: 3}), "tester")
		require.NoError(t, err)
	})

	t.Run("an unknown sibling under the назначение blocks the sorted line too", func(t *testing.T) {
		// Clearing the ворсовая line's direction leaves назначение main half-answered, and half an
		// answer is a guess — the save stops until somebody sets it.
		card, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		cleared := make([]entity.TechCardBomItem, len(bom))
		copy(cleared, bom)
		for i := range cleared {
			if cleared[i].LineKey == lineMainNap {
				cleared[i].FabricDirection = sql.NullString{}
			}
		}
		require.NoError(t, T.UpdateTechCard(ctx, tcID, &entity.TechCardInsert{
			Name: "Direction Style", Stage: entity.TechCardStageProto, StyleNumber: ns("FDIR-1"),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			SizeIds:  []int{szA},
			BomItems: cleared,
		}, card.LockVersion))

		var ve *entity.ValidationError
		_, err = T.SaveMarker(ctx, tcID, 0, ins("main без соседа", lineMainAny, v3clean,
			entity.MarkerLayoutFacts{SchemaVersion: 3}), "tester")
		require.ErrorAs(t, err, &ve)
		require.Equal(t, "bom_items.fabric_direction", ve.Field)
		require.Contains(t, ve.Message, "Основная ворсовая")
	})
}

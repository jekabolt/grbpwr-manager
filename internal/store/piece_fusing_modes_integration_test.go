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

// TestPieceFusingModesRoundTrip proves the per-ring grade mode with its anchor and the per-end strip
// modes survive a real write and read, and that a piece on the defaults stores the JSON it stored
// before the modes existed.
func TestPieceFusingModesRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	d := decimal.RequireFromString
	ring := func(x0, y0, x1, y1 string) entity.TechCardPieceFusingSpline {
		return entity.TechCardPieceFusingSpline{Points: []entity.TechCardPieceFusingSplinePoint{
			{X: d(x0), Y: d(y0), Corner: true}, {X: d(x1), Y: d(y0), Corner: true},
			{X: d(x1), Y: d(y1), Corner: true}, {X: d(x0), Y: d(y1), Corner: true},
		}}
	}
	spline := ring("0", "0", "20", "10")
	spline.GradeMode = entity.PieceFusingGradeModeFollowLine
	spline.Anchor = &entity.TechCardPieceFusingAnchor{X: d("12.5"), Y: d("-3")}
	island := ring("30", "0", "40", "10")
	island.GradeMode = entity.PieceFusingGradeModeGrow

	card := &entity.TechCardInsert{
		Name: "Fusing Modes Style", Stage: entity.TechCardStageProto, StyleNumber: ns("FMODE-1"),
		Purpose:         entity.TechCardPurposeSellable,
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		Pieces: []entity.TechCardPiece{
			{
				LineKey: "ZONE", Name: "Front", PiecesPerGarment: 1, Grainline: "lengthwise", Fused: true,
				FusingMode: ns(string(entity.PieceFusingModeZone)),
				FusingZone: &entity.TechCardPieceFusingZone{Spline: &spline, Islands: []entity.TechCardPieceFusingSpline{island}},
			},
			{
				LineKey: "STRIP", Name: "Placket", PiecesPerGarment: 1, Grainline: "lengthwise", Fused: true,
				FusingMode: ns(string(entity.PieceFusingModeStrip)),
				FusingEdges: []entity.TechCardPieceFusingEdge{
					{Edge: 0, StartMm: d("10"), EndMm: d("10")},
					{Edge: 2, StartMm: d("5"), EndMm: d("35.5"),
						StartMode: entity.PieceFusingEndModeNotch, EndMode: entity.PieceFusingEndModeShare},
				},
			},
			{
				LineKey: "PLAIN", Name: "Back", PiecesPerGarment: 1, Grainline: "lengthwise", Fused: true,
				FusingMode: ns(string(entity.PieceFusingModeZone)),
				FusingZone: func() *entity.TechCardPieceFusingZone {
					r := ring("0", "0", "20", "10")
					return &entity.TechCardPieceFusingZone{Spline: &r}
				}(),
			},
		},
	}
	tcID, err := T.AddTechCard(ctx, card)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	got, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	byKey := map[string]entity.TechCardPiece{}
	for _, p := range got.Pieces {
		byKey[p.LineKey] = p
	}

	zone := byKey["ZONE"].FusingZone
	require.NotNil(t, zone)
	require.Equal(t, entity.PieceFusingGradeModeFollowLine, zone.Spline.GradeMode)
	require.NotNil(t, zone.Spline.Anchor)
	require.Equal(t, "12.5", zone.Spline.Anchor.X.String())
	require.Equal(t, "-3", zone.Spline.Anchor.Y.String())
	require.Len(t, zone.Islands, 1)
	require.Equal(t, entity.PieceFusingGradeModeGrow, zone.Islands[0].GradeMode)
	require.Nil(t, zone.Islands[0].Anchor)

	edges := byKey["STRIP"].FusingEdges
	require.Len(t, edges, 2)
	require.Equal(t, entity.PieceFusingEndModeMm, edges[0].StartMode)
	require.Equal(t, entity.PieceFusingEndModeMm, edges[0].EndMode)
	require.Equal(t, entity.PieceFusingEndModeNotch, edges[1].StartMode)
	require.Equal(t, entity.PieceFusingEndModeShare, edges[1].EndMode)
	require.Equal(t, "35.5", edges[1].EndMm.String())

	plain := byKey["PLAIN"].FusingZone
	require.NotNil(t, plain)
	require.Equal(t, entity.PieceFusingGradeModeFixed, plain.Spline.GradeMode)
	require.Nil(t, plain.Spline.Anchor)
	var plainHasModeKeys, stripDefaultHasModeKeys int
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT JSON_CONTAINS_PATH(fusing_zone, 'one', '$.spline.grade_mode', '$.spline.anchor')
		FROM tech_card_piece WHERE tech_card_id = ? AND line_key = 'PLAIN'`, tcID).Scan(&plainHasModeKeys))
	require.Zero(t, plainHasModeKeys, "a fixed ring must store none of the new keys")
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT JSON_CONTAINS_PATH(fusing_edges, 'one', '$[0].start_mode', '$[0].end_mode')
		FROM tech_card_piece WHERE tech_card_id = ? AND line_key = 'STRIP'`, tcID).Scan(&stripDefaultHasModeKeys))
	require.Zero(t, stripDefaultHasModeKeys, "a millimetre end must store none of the new keys")
}

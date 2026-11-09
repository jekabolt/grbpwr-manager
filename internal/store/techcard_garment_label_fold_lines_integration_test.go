package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// Fold lines (0460) round-trip through the store, and a save from a client that does not send them
// keeps the stored lines once the write path's carry has run (dto.CarryStoredGarmentLabelGeometry is
// what prepareTechCardWriteFrom calls with the stored card).
func TestTechCardGarmentLabelFoldLines(t *testing.T) {
	requireGarmentLabelGeometryDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cfg := *testCfg
	cfg.Automigrate = true
	st, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer st.Close()
	T := st.TechCards()
	ns := func(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }

	cardID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "LABEL FOLD LINES", Stage: entity.TechCardStageProto, StyleNumber: ns("LBF-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft, LabelsAware: true,
		GarmentLabels: []entity.TechCardGarmentLabel{
			{
				Key: "size", LineKey: geometryLabelKey, QtyPerGarment: 1,
				HeightMm: decimal.NewNullDecimal(decimal.RequireFromString("100")), Fold: ns("ends"),
				FoldLinesMm: "12,88",
			},
			{Key: "brand", LineKey: geometryPieceKey, QtyPerGarment: 1},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), cardID) })

	read, err := T.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	require.Len(t, read.GarmentLabels, 2)
	require.Equal(t, "12,88", read.GarmentLabels[0].FoldLinesMm)
	require.Equal(t, "", read.GarmentLabels[1].FoldLinesMm, "a label without lines reads the column default")

	// An older client: same labels, no fold_lines_mm on the wire.
	omitted := read.TechCardInsert
	omitted.LabelsAware = true
	omitted.GarmentLabels = []entity.TechCardGarmentLabel{
		{Key: "size", LineKey: geometryLabelKey, QtyPerGarment: 1,
			HeightMm: decimal.NewNullDecimal(decimal.RequireFromString("100")), Fold: ns("ends"), FoldLinesOmitted: true},
		{Key: "brand", LineKey: geometryPieceKey, QtyPerGarment: 1, FoldLinesOmitted: true},
	}
	dto.CarryStoredGarmentLabelGeometry(&read.TechCardInsert, &omitted)
	require.NoError(t, T.UpdateTechCard(ctx, cardID, &omitted, read.LockVersion))
	kept, err := T.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	require.Equal(t, "12,88", kept.GarmentLabels[0].FoldLinesMm, "absent keeps the stored lines")
	require.Equal(t, "", kept.GarmentLabels[1].FoldLinesMm)

	// A current client clears the lines with an explicit empty list.
	cleared := kept.TechCardInsert
	cleared.LabelsAware = true
	cleared.GarmentLabels = append([]entity.TechCardGarmentLabel(nil), kept.GarmentLabels...)
	cleared.GarmentLabels[0].FoldLinesMm = ""
	dto.CarryStoredGarmentLabelGeometry(&kept.TechCardInsert, &cleared)
	require.NoError(t, T.UpdateTechCard(ctx, cardID, &cleared, kept.LockVersion))
	after, err := T.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	require.Equal(t, "", after.GarmentLabels[0].FoldLinesMm, "\"\" clears the lines")
}

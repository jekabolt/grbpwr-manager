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

// fold_axis and pages (0466) round-trip through the store; a save from a client that sends neither keeps
// the stored values once the carry has run; with axis "width" lines past the height but inside the width
// survive.
func TestTechCardGarmentLabelFoldAxisPages(t *testing.T) {
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
	dec := func(v string) decimal.NullDecimal { return decimal.NewNullDecimal(decimal.RequireFromString(v)) }

	cardID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "LABEL FOLD AXIS", Stage: entity.TechCardStageProto, StyleNumber: ns("LBX-466"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft, LabelsAware: true,
		GarmentLabels: []entity.TechCardGarmentLabel{
			{
				Key: "size", LineKey: geometryLabelKey, QtyPerGarment: 1,
				WidthMm: dec("100"), HeightMm: dec("30"), Fold: ns("ends"),
				FoldAxis: "width", Pages: 6,
				// Parse-time normalisation against the width keeps 40 and 90 although the height is 30.
				FoldLinesMm:   dto.NormalizeGarmentLabelFoldLines("40,90,150", dec("100")),
				StitchLinesMm: dto.NormalizeGarmentLabelStitchLines("0,90", dec("100")),
			},
			{Key: "brand", LineKey: geometryPieceKey, QtyPerGarment: 1},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), cardID) })

	read, err := T.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	require.Len(t, read.GarmentLabels, 2)
	require.Equal(t, "width", read.GarmentLabels[0].FoldAxis)
	require.Equal(t, 6, read.GarmentLabels[0].Pages)
	require.Equal(t, "40,90", read.GarmentLabels[0].FoldLinesMm, "lines over the 30 mm height survive on the width axis")
	require.Equal(t, "0,90", read.GarmentLabels[0].StitchLinesMm)
	require.Equal(t, "", read.GarmentLabels[1].FoldAxis, "column default")
	require.Equal(t, 1, read.GarmentLabels[1].Pages, "column default (and 0 on the entity is written as 1)")

	// An older client: same labels, neither field on the wire.
	omitted := read.TechCardInsert
	omitted.LabelsAware = true
	omitted.GarmentLabels = []entity.TechCardGarmentLabel{
		{Key: "size", LineKey: geometryLabelKey, QtyPerGarment: 1, WidthMm: dec("100"), HeightMm: dec("30"), Fold: ns("ends"),
			FoldAxisOmitted: true, PagesOmitted: true, FoldLinesMm: "40,90", StitchLinesMm: "0,90"},
		{Key: "brand", LineKey: geometryPieceKey, QtyPerGarment: 1, FoldAxisOmitted: true, PagesOmitted: true},
	}
	dto.CarryStoredGarmentLabelGeometry(&read.TechCardInsert, &omitted)
	require.NoError(t, T.UpdateTechCard(ctx, cardID, &omitted, read.LockVersion))
	kept, err := T.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	require.Equal(t, "width", kept.GarmentLabels[0].FoldAxis, "absent keeps the stored axis")
	require.Equal(t, 6, kept.GarmentLabels[0].Pages, "absent keeps the stored pages")
	require.Equal(t, "40,90", kept.GarmentLabels[0].FoldLinesMm)
	require.Equal(t, 1, kept.GarmentLabels[1].Pages)

	// A current client resets the axis and clamps the pages.
	changed := kept.TechCardInsert
	changed.LabelsAware = true
	changed.GarmentLabels = append([]entity.TechCardGarmentLabel(nil), kept.GarmentLabels...)
	changed.GarmentLabels[0].FoldAxis = ""
	changed.GarmentLabels[0].Pages = 99
	changed.GarmentLabels[0].FoldLinesMm = dto.NormalizeGarmentLabelFoldLines("40,90", dec("30"))
	changed.GarmentLabels[0].Pages = dto.NormalizeGarmentLabelPages(changed.GarmentLabels[0].Pages)
	dto.CarryStoredGarmentLabelGeometry(&kept.TechCardInsert, &changed)
	require.NoError(t, T.UpdateTechCard(ctx, cardID, &changed, kept.LockVersion))
	after, err := T.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	require.Equal(t, "", after.GarmentLabels[0].FoldAxis)
	require.Equal(t, 12, after.GarmentLabels[0].Pages)
	require.Equal(t, "", after.GarmentLabels[0].FoldLinesMm, "against the 30 mm height 40 and 90 are gone")
}

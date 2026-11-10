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

// The piece fields and the prints (0467) round-trip through the store; a save from a client that knows
// nothing of 0467 keeps both once the carry has run; a current client replaces the prints; deleting the
// label takes its prints with it (cascade).
func TestTechCardGarmentLabelPiecePrints(t *testing.T) {
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
	dec := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }
	nd := func(v string) decimal.NullDecimal { return decimal.NewNullDecimal(dec(v)) }
	const fabricLine = "01LABELPIECEFABRICLINE0001"

	fileA := insertLibraryFileFixture(ctx, t, "print-a.svg", 10, "test-super")
	fileB := insertLibraryFileFixture(ctx, t, "print-b.svg", 10, "test-super")

	cardID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "LABEL PIECE", Stage: entity.TechCardStageProto, StyleNumber: ns("LBP-467"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft, LabelsAware: true,
		BomItems: []entity.TechCardBomItem{{LineKey: fabricLine, Section: entity.BomSectionFabric, Name: "cotton twill"}},
		GarmentLabels: []entity.TechCardGarmentLabel{
			{
				Key: "brand", LineKey: geometryLabelKey, QtyPerGarment: 1, WidthMm: nd("40"), HeightMm: nd("20"),
				Attachment: ns("on_fabric_piece"), PieceBomLineKey: fabricLine,
				PieceWidthMm: dec("60"), PieceHeightMm: dec("40"), PieceSeamMm: dec("10"),
				LabelXMm: dec("10"), LabelYMm: dec("12.5"), LabelOnPiece: "4_sides",
				Prints: []entity.TechCardGarmentLabelPrint{
					{Name: "logo", LibraryFileId: sql.NullInt32{Int32: int32(fileA), Valid: true}, Method: "screen",
						Colour: "black", WidthMm: dec("20"), HeightMm: dec("8"), XMm: dec("10"), YMm: dec("6")},
					{Name: "size", Method: "pad_print", WidthMm: dec("5"), HeightMm: dec("5"), XMm: dec("0"), YMm: dec("0")},
				},
			},
			{Key: "size", LineKey: geometryPieceKey, QtyPerGarment: 1},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), cardID) })

	read, err := T.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	require.Len(t, read.GarmentLabels, 2)
	l := read.GarmentLabels[0]
	require.True(t, l.PieceBomItemId.Valid, "the key of a line born in the same save resolves to its id")
	require.Equal(t, fabricLine, l.PieceBomLineKey)
	require.True(t, l.PieceWidthMm.Equal(dec("60")))
	require.True(t, l.PieceHeightMm.Equal(dec("40")))
	require.True(t, l.PieceSeamMm.Equal(dec("10")))
	require.True(t, l.LabelXMm.Equal(dec("10")))
	require.True(t, l.LabelYMm.Equal(dec("12.5")))
	require.Equal(t, "4_sides", l.LabelOnPiece)
	require.Len(t, l.Prints, 2)
	require.Equal(t, "logo", l.Prints[0].Name)
	require.EqualValues(t, fileA, l.Prints[0].LibraryFileId.Int32)
	require.True(t, l.Prints[0].XMm.Equal(dec("10")))
	require.Equal(t, "size", l.Prints[1].Name, "display order is list order")
	require.False(t, l.Prints[1].LibraryFileId.Valid)
	empty := read.GarmentLabels[1]
	require.False(t, empty.PieceBomItemId.Valid)
	require.True(t, empty.PieceWidthMm.IsZero(), "column default")
	require.Equal(t, "", empty.LabelOnPiece)
	require.Empty(t, empty.Prints)

	// A client that knows nothing of 0467: every piece field and the prints absent.
	all := entity.GarmentLabelPieceOmitted{BomItem: true, Width: true, Height: true, Seam: true, LabelX: true, LabelY: true, LabelOnPiece: true}
	omitted := read.TechCardInsert
	omitted.LabelsAware = true
	omitted.GarmentLabels = []entity.TechCardGarmentLabel{
		{Key: "brand", LineKey: geometryLabelKey, QtyPerGarment: 1, WidthMm: nd("40"), HeightMm: nd("20"),
			Attachment: ns("on_fabric_piece"), PieceOmitted: all, PrintsOmitted: true},
		{Key: "size", LineKey: geometryPieceKey, QtyPerGarment: 1, PieceOmitted: all, PrintsOmitted: true},
	}
	dto.CarryStoredGarmentLabelGeometry(&read.TechCardInsert, &omitted)
	require.NoError(t, T.UpdateTechCard(ctx, cardID, &omitted, read.LockVersion))
	kept, err := T.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	k := kept.GarmentLabels[0]
	require.Equal(t, l.PieceBomItemId, k.PieceBomItemId, "absent keeps the stored fabric")
	require.True(t, k.PieceWidthMm.Equal(dec("60")))
	require.True(t, k.LabelYMm.Equal(dec("12.5")))
	require.Equal(t, "4_sides", k.LabelOnPiece)
	require.Len(t, k.Prints, 2, "absent label_on_piece keeps the stored prints")
	require.EqualValues(t, fileA, k.Prints[0].LibraryFileId.Int32)

	// A current client: replace-all of the prints, piece cleared.
	changed := kept.TechCardInsert
	changed.LabelsAware = true
	changed.GarmentLabels = append([]entity.TechCardGarmentLabel(nil), kept.GarmentLabels...)
	changed.GarmentLabels[0].PieceBomItemId, changed.GarmentLabels[0].PieceBomLineKey = sql.NullInt32{}, ""
	changed.GarmentLabels[0].PieceWidthMm, changed.GarmentLabels[0].PieceHeightMm = decimal.Zero, decimal.Zero
	changed.GarmentLabels[0].LabelOnPiece = "top_edge"
	changed.GarmentLabels[0].Prints = []entity.TechCardGarmentLabelPrint{
		{Name: "care", LibraryFileId: sql.NullInt32{Int32: int32(fileB), Valid: true}, Method: "heat_transfer",
			WidthMm: dec("30"), HeightMm: dec("10"), XMm: dec("5"), YMm: dec("5")},
	}
	dto.CarryStoredGarmentLabelGeometry(&kept.TechCardInsert, &changed)
	require.NoError(t, T.UpdateTechCard(ctx, cardID, &changed, kept.LockVersion))
	after, err := T.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	a := after.GarmentLabels[0]
	require.False(t, a.PieceBomItemId.Valid)
	require.True(t, a.PieceWidthMm.IsZero())
	require.Equal(t, "top_edge", a.LabelOnPiece)
	require.Len(t, a.Prints, 1, "the prints were replaced, not appended")
	require.Equal(t, "care", a.Prints[0].Name)
	require.EqualValues(t, fileB, a.Prints[0].LibraryFileId.Int32)

	// Deleting the label takes its prints with it.
	var labelID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM tech_card_garment_label WHERE tech_card_id = ? AND line_key = ?`, cardID, geometryLabelKey).Scan(&labelID))
	gone := after.TechCardInsert
	gone.LabelsAware = true
	gone.GarmentLabels = []entity.TechCardGarmentLabel{after.GarmentLabels[1]}
	require.NoError(t, T.UpdateTechCard(ctx, cardID, &gone, after.LockVersion))
	var left int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tech_card_garment_label_print WHERE label_id = ?`, labelID).Scan(&left))
	require.Zero(t, left, "ON DELETE CASCADE from the label")
}

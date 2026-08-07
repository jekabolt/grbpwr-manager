package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/encoding/protojson"
)

// layTestBlob marshals a layout the way the save path stores it, so these tests read the same bytes
// the server would. Hand-written JSON would test the fixture rather than the parse.
func layTestBlob(t *testing.T, schema int32, composition map[int]int, pieceKey string, pieceSize, asDrawn, mirrored int) string {
	t.Helper()
	l := &pb_common.TechCardMarkerLayout{SchemaVersion: schema}
	for sizeID, qty := range composition {
		l.Composition = append(l.Composition, &pb_common.TechCardMarkerCompositionEntry{
			SizeId: int32(sizeID), Quantity: int32(qty),
		})
	}
	l.Pieces = []*pb_common.TechCardMarkerPiece{{
		PieceId: 1, Name: "полочка", PieceLineKey: pieceKey, SizeId: int32(pieceSize), Quantity: 1,
	}}
	for i := 0; i < asDrawn; i++ {
		l.Placements = append(l.Placements, &pb_common.TechCardMarkerPlacement{PieceId: 1})
	}
	for i := 0; i < mirrored; i++ {
		l.Placements = append(l.Placements, &pb_common.TechCardMarkerPlacement{PieceId: 1, Flipped: true})
	}
	b, err := protojson.Marshal(l)
	require.NoError(t, err)
	return string(b)
}

// TestDistilLayPlanMarkerFoldsTheLegacyСостав pins the thing the yield distiller explicitly leaves to
// its caller: a blob of schema < 4 carries NO состав, because before Ф2 the состав lived on the marker
// SUMMARY. Without the fold TotalUnits stays 0, and every per-layer question about that marker comes
// back UNKNOWN — a лай that cuts perfectly well would render grey forever.
//
// The control cases are the point of the test as much as the happy one: the fold must not invent a
// состав it does not have, and must never let a zero denominator reach the arithmetic.
func TestDistilLayPlanMarkerFoldsTheLegacyComposition(t *testing.T) {
	legacySummary := func(sizeID, sets int) entity.TechCardMarkerSummary {
		return entity.TechCardMarkerSummary{
			Id: 900, Name: "основная 40-42", TechCardId: 7,
			SizeId: sql.NullInt64{Int64: int64(sizeID), Valid: sizeID > 0},
			Sets:   sql.NullInt64{Int64: int64(sets), Valid: sets > 0},
		}
	}

	t.Run("schema 3 blob + summary состав → known composition, positive TotalUnits", func(t *testing.T) {
		m := &entity.TechCardMarker{
			TechCardMarkerSummary: legacySummary(5, 4),
			// 4 as-drawn placements of a size-agnostic piece: the состав is what splits them per size.
			Layout: layTestBlob(t, 3, nil, "P1", 0, 4, 0),
		}
		got := DistilLayPlanMarker(m)
		require.NoError(t, got.YieldErr)
		require.NotNil(t, got.Yield)
		require.True(t, got.Yield.CompositionKnown(), "the summary's состав must have been folded in")
		require.Equal(t, 4, got.Yield.TotalUnits)
		require.Equal(t, map[int]int{5: 4}, got.Yield.Composition)
		require.Empty(t, got.Caveats)

		// The whole reason the fold matters: WITH it the piece answers a number, and the number is the
		// exact proportional split (4 × 4 / 4), not a floor and not a guess.
		inst := got.Yield.PerLayerInstances("P1", 5)
		require.True(t, inst.Known, "a folded состав makes the piece answerable")
		require.Equal(t, 4, inst.Counts.AsDrawn)
		require.Empty(t, inst.Caveats)

		// And a size the состав does not cut is an honest ZERO, not an unknown: the cloth was laid,
		// that size was not on it.
		other := got.Yield.PerLayerInstances("P1", 6)
		require.True(t, other.Known)
		require.Equal(t, 0, other.Counts.Total())
	})

	t.Run("no состав anywhere → UNKNOWN with a caveat, never a division and never a phantom 1", func(t *testing.T) {
		m := &entity.TechCardMarker{
			TechCardMarkerSummary: entity.TechCardMarkerSummary{Id: 901, Name: "безымянная", TechCardId: 7},
			Layout:                layTestBlob(t, 3, nil, "P1", 0, 4, 0),
		}
		got := DistilLayPlanMarker(m)
		require.NoError(t, got.YieldErr)
		require.NotNil(t, got.Yield)
		require.False(t, got.Yield.CompositionKnown())
		require.Equal(t, 0, got.Yield.TotalUnits, "nothing may fill in a garment count nobody stated")
		require.NotEmpty(t, got.Caveats, "the silence has to reach the operator")

		inst := got.Yield.PerLayerInstances("P1", 5)
		require.False(t, inst.Known, "UNKNOWN, not zero and not one")
		require.Equal(t, 0, inst.Counts.Total())
	})

	t.Run("schema 4 blob keeps its own состав; the summary must not override it", func(t *testing.T) {
		m := &entity.TechCardMarker{
			// The row still carries the legacy pair — the two must not fight, and the measured one wins.
			TechCardMarkerSummary: legacySummary(5, 4),
			Layout:                layTestBlob(t, 4, map[int]int{5: 2, 6: 1}, "P1", 0, 6, 0),
		}
		got := DistilLayPlanMarker(m)
		require.NoError(t, got.YieldErr)
		require.Equal(t, 3, got.Yield.TotalUnits)
		require.Equal(t, map[int]int{5: 2, 6: 1}, got.Yield.Composition)
		require.Empty(t, got.Caveats)
	})

	t.Run("an unparsable blob is an error, not an empty marker", func(t *testing.T) {
		got := DistilLayPlanMarker(&entity.TechCardMarker{
			TechCardMarkerSummary: legacySummary(5, 4), Layout: "{not json",
		})
		require.Error(t, got.YieldErr)
		require.Nil(t, got.Yield, "nil yield reads as UNKNOWN downstream; an empty one would read as «cuts nothing»")
	})
}

// TestLayArticleMaterialIdUsesTheRecipeResolver pins that a настил's article is the one the колорвей
// pins on its slot, that it falls back to the slot's own default, and that it goes through the SAME
// slot resolution the recipe uses — including the LEGACY POSITIONAL index, which is the form that
// already produced an empty material plan on beta (§14 п.5).
func TestLayArticleMaterialIdUsesTheRecipeResolver(t *testing.T) {
	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{
		{Id: 5, LineKey: "B1", Section: entity.BomSectionFabric, Name: "основная",
			MaterialId: sql.NullInt64{Int64: 42, Valid: true}},
		{Id: 6, LineKey: "B2", Section: entity.BomSectionLining, Name: "подкладка",
			MaterialId: sql.NullInt64{Int64: 43, Valid: true}},
	}
	card.Colorways = []entity.TechCardColorway{{
		ProductId: sql.NullInt32{Int32: 100, Valid: true},
		Usages: []entity.TechCardColorwayUsage{
			// Pinned by durable FK: this colourway buys a different article for the main cloth.
			{BomItemId: sql.NullInt64{Int64: 5, Valid: true}, MaterialId: sql.NullInt64{Int64: 77, Valid: true}},
			// Referenced by the legacy POSITIONAL index only (0109:39) — index 1 is the lining.
			{BomItemIndex: sql.NullInt32{Int32: 1, Valid: true}},
		},
	}}

	require.Equal(t, 77, LayArticleMaterialId(card, 100, 5), "the colourway's pin wins")
	require.Equal(t, 43, LayArticleMaterialId(card, 100, 6),
		"a usage that only carries the positional index still resolves, and falls back to the slot's default article")
	require.Equal(t, 42, LayArticleMaterialId(card, 200, 5), "an unknown colourway falls back to the slot default")
	require.Equal(t, 0, LayArticleMaterialId(card, 100, 999), "an unknown slot answers 0, never a guess")

	require.Equal(t, []int{42, 77}, LayPlanMaterialIds(card, []entity.ProductionRunLay{
		{ColorwayId: 100, BomItemId: sql.NullInt64{Int64: 5, Valid: true}},
		{ColorwayId: 200, BomItemId: sql.NullInt64{Int64: 5, Valid: true}},
		// A настил that lost its slot names no article at all.
		{ColorwayId: 100},
	}), "the fetch list is derived from the same resolver the projection reads")
}

// TestConvertPbProductionRunLayInsertToEntity pins the wire → domain edges that decide whether a save
// is even attempted.
func TestConvertPbProductionRunLayInsertToEntity(t *testing.T) {
	t.Run("UNSPECIFIED mode is refused, never read as face up", func(t *testing.T) {
		_, err := ConvertPbProductionRunLayInsertToEntity(&pb_common.ProductionRunLayInsert{ColorwayId: 100})
		require.Error(t, err)
		var ve *entity.ValidationError
		require.ErrorAs(t, err, &ve)
		require.Equal(t, "lay.mode", ve.Field)
	})

	t.Run("a full payload converts, and an empty note stays NULL", func(t *testing.T) {
		got, err := ConvertPbProductionRunLayInsertToEntity(&pb_common.ProductionRunLayInsert{
			LayKey:     " 01J0000000000000000000000A ",
			ColorwayId: 100,
			BomLineKey: "B1",
			Mode:       pb_common.ProductionLayMode_PRODUCTION_LAY_MODE_FACE_TO_FACE,
			EndLossCm:  &pb_decimal.Decimal{Value: "2.5"},
			Sections: []*pb_common.ProductionRunLaySectionInsert{
				{SectionKey: "S1", MarkerId: 900, Plies: 20, Position: 1},
			},
		})
		require.NoError(t, err)
		require.Equal(t, "01J0000000000000000000000A", got.LayKey, "trimmed")
		require.Equal(t, entity.ProductionLayModeFaceToFace, got.Mode)
		require.True(t, got.EndLossCm.Equal(decimal.RequireFromString("2.5")))
		require.False(t, got.Note.Valid, "an empty note is NULL, not an empty string")
		require.Len(t, got.Sections, 1)
		require.Equal(t, 20, got.Sections[0].Plies)
	})

	t.Run("a non-numeric end loss names the field", func(t *testing.T) {
		_, err := ConvertPbProductionRunLayInsertToEntity(&pb_common.ProductionRunLayInsert{
			ColorwayId: 100,
			Mode:       pb_common.ProductionLayMode_PRODUCTION_LAY_MODE_FACE_UP,
			EndLossCm:  &pb_decimal.Decimal{Value: "две"},
		})
		require.Error(t, err)
		var ve *entity.ValidationError
		require.ErrorAs(t, err, &ve)
		require.Equal(t, "lay.end_loss_cm", ve.Field)
	})
}

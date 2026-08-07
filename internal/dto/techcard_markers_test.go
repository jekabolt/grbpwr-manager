package dto

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

func validMarkerInsertPb() *pb_common.TechCardMarkerInsert {
	return &pb_common.TechCardMarkerInsert{
		SizeId:        3,
		Name:          "  M · основная  ",
		Source:        "auto",
		BomLineKey:    " 01J0000000000000000000000K ",
		FabricWidthCm: &pb_decimal.Decimal{Value: "140"},
		GapCm:         &pb_decimal.Decimal{Value: "0.5"},
		EdgeMarginCm:  &pb_decimal.Decimal{Value: "1"},
		Sets:          4,
		UsedLengthCm:  &pb_decimal.Decimal{Value: "512.4"},
		EfficiencyPct: &pb_decimal.Decimal{Value: "73.5"},
		PlacedCount:   12,
		TotalCount:    12,
	}
}

// The dto parses FORM only — trim/normalise, bounds, enum membership. Everything the database has
// to witness (size membership, BOM identity, released card, name uniqueness) is the store's.
func TestConvertPbTechCardMarkerInsertToEntity(t *testing.T) {
	t.Run("valid payload normalises", func(t *testing.T) {
		out, err := ConvertPbTechCardMarkerInsertToEntity(validMarkerInsertPb())
		require.NoError(t, err)
		require.Equal(t, "M · основная", out.Name)
		require.Equal(t, "01J0000000000000000000000K", out.BomLineKey)
		require.Equal(t, entity.MarkerSourceAuto, out.Source)
		require.True(t, out.EfficiencyPct.Valid)
		require.Equal(t, "512.4", out.UsedLengthCm.String())
		// A payload with size_id + sets and no layout.composition is a STALE BUNDLE: it is stored in
		// the legacy shape AND projected into a one-entry состав, so every reader sees one format.
		require.Equal(t, sql.NullInt64{Int64: 3, Valid: true}, out.SizeId)
		require.Equal(t, sql.NullInt64{Int64: 4, Valid: true}, out.Sets)
		require.Equal(t, []entity.MarkerCompositionEntry{{SizeId: 3, Quantity: 4}}, out.Composition)
		require.Equal(t, 4, entity.TotalUnitsOf(out.Composition))
	})

	t.Run("a состав replaces size_id/sets entirely", func(t *testing.T) {
		pb := validMarkerInsertPb()
		pb.Layout = &pb_common.TechCardMarkerLayout{
			SchemaVersion: 4,
			// Deliberately out of order: the состав is a SET, and the server canonicalises it so the
			// stored blob is a function of the раскладка and not of the client's form order.
			Composition: []*pb_common.TechCardMarkerCompositionEntry{
				{SizeId: 7, Quantity: 2}, {SizeId: 3, Quantity: 1},
			},
		}
		out, err := ConvertPbTechCardMarkerInsertToEntity(pb)
		require.NoError(t, err)
		require.False(t, out.SizeId.Valid, "a marker with a состав stores NULL, not a representative size")
		require.False(t, out.Sets.Valid)
		require.Equal(t, []entity.MarkerCompositionEntry{{SizeId: 3, Quantity: 1}, {SizeId: 7, Quantity: 2}},
			out.Composition)
		require.Equal(t, 3, entity.TotalUnitsOf(out.Composition), "total_units is Σ quantity, derived by the server")
	})

	t.Run("empty source defaults to auto", func(t *testing.T) {
		pb := validMarkerInsertPb()
		pb.Source = ""
		out, err := ConvertPbTechCardMarkerInsertToEntity(pb)
		require.NoError(t, err)
		require.Equal(t, entity.MarkerSourceAuto, out.Source)
	})

	t.Run("efficiency may be absent", func(t *testing.T) {
		pb := validMarkerInsertPb()
		pb.EfficiencyPct = nil
		out, err := ConvertPbTechCardMarkerInsertToEntity(pb)
		require.NoError(t, err)
		require.False(t, out.EfficiencyPct.Valid)
	})

	rejects := []struct {
		name   string
		mutate func(pb *pb_common.TechCardMarkerInsert)
		want   string
	}{
		{"nil marker", nil, "marker is required"},
		// FAIL-CLOSED: neither a состав nor the legacy pair. Assuming «one комплект» would report the
		// whole spread as one garment's norm, and a client writes that straight into a recipe.
		{"no состав and no legacy pair", func(pb *pb_common.TechCardMarkerInsert) { pb.SizeId = 0 }, "состав"},
		{"legacy size without sets", func(pb *pb_common.TechCardMarkerInsert) { pb.Sets = 0 }, "состав"},
		{"blank name", func(pb *pb_common.TechCardMarkerInsert) { pb.Name = "   " }, "name is required"},
		// The column is VARCHAR(191) and MySQL counts CHARACTERS, so the cap counts runes —
		// 96 Cyrillic characters are 192 bytes but must be accepted (see the accept case below).
		// characters are already 192 bytes.
		{"name over 191 characters", func(pb *pb_common.TechCardMarkerInsert) { pb.Name = strings.Repeat("ю", 192) }, "191 characters"},
		{"unknown source", func(pb *pb_common.TechCardMarkerInsert) { pb.Source = "guessed" }, "source"},
		{"missing width", func(pb *pb_common.TechCardMarkerInsert) { pb.FabricWidthCm = nil }, "fabric_width_cm"},
		{"zero width", func(pb *pb_common.TechCardMarkerInsert) { pb.FabricWidthCm = &pb_decimal.Decimal{Value: "0"} }, "fabric_width_cm"},
		{"negative gap", func(pb *pb_common.TechCardMarkerInsert) { pb.GapCm = &pb_decimal.Decimal{Value: "-0.5"} }, "gap_cm"},
		{"negative margin", func(pb *pb_common.TechCardMarkerInsert) { pb.EdgeMarginCm = &pb_decimal.Decimal{Value: "-1"} }, "edge_margin_cm"},
		{"missing used length", func(pb *pb_common.TechCardMarkerInsert) { pb.UsedLengthCm = nil }, "used_length_cm"},
		{"zero used length", func(pb *pb_common.TechCardMarkerInsert) { pb.UsedLengthCm = &pb_decimal.Decimal{Value: "0"} }, "used_length_cm"},
		{"efficiency over 100", func(pb *pb_common.TechCardMarkerInsert) { pb.EfficiencyPct = &pb_decimal.Decimal{Value: "100.01"} }, "efficiency_pct"},
		{"negative placed", func(pb *pb_common.TechCardMarkerInsert) { pb.PlacedCount = -1 }, "placed_count"},
		{"zero total", func(pb *pb_common.TechCardMarkerInsert) { pb.TotalCount = 0 }, "total_count"},
	}
	for _, c := range rejects {
		t.Run(c.name, func(t *testing.T) {
			var pb *pb_common.TechCardMarkerInsert
			if c.mutate != nil {
				pb = validMarkerInsertPb()
				c.mutate(pb)
			}
			_, err := ConvertPbTechCardMarkerInsertToEntity(pb)
			require.Error(t, err)
			require.Contains(t, err.Error(), c.want)
		})
	}
}

// The composition shape is refused in dto, before anything reaches the database.
func TestMarkerCompositionShapeRefusals(t *testing.T) {
	with := func(entries ...*pb_common.TechCardMarkerCompositionEntry) *pb_common.TechCardMarkerInsert {
		pb := validMarkerInsertPb()
		pb.Layout = &pb_common.TechCardMarkerLayout{SchemaVersion: 4, Composition: entries}
		return pb
	}
	cases := []struct {
		name string
		pb   *pb_common.TechCardMarkerInsert
		want string
	}{
		{"quantity zero", with(&pb_common.TechCardMarkerCompositionEntry{SizeId: 3, Quantity: 0}), "quantity must be at least 1"},
		{"negative quantity", with(&pb_common.TechCardMarkerCompositionEntry{SizeId: 3, Quantity: -1}), "quantity must be at least 1"},
		{"missing size", with(&pb_common.TechCardMarkerCompositionEntry{SizeId: 0, Quantity: 2}), "size_id is required"},
		// A duplicate is refused rather than summed: summing would make two different payloads mean
		// the same раскладка and the blob would stop being a function of the состав.
		{"duplicate size", with(
			&pb_common.TechCardMarkerCompositionEntry{SizeId: 3, Quantity: 1},
			&pb_common.TechCardMarkerCompositionEntry{SizeId: 3, Quantity: 2}), "twice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ConvertPbTechCardMarkerInsertToEntity(c.pb)
			require.Error(t, err)
			require.Contains(t, err.Error(), c.want)
		})
	}

	t.Run("more sizes than the ceiling", func(t *testing.T) {
		entries := make([]*pb_common.TechCardMarkerCompositionEntry, 0, entity.MaxMarkerCompositionSizes+1)
		for i := 1; i <= entity.MaxMarkerCompositionSizes+1; i++ {
			entries = append(entries, &pb_common.TechCardMarkerCompositionEntry{SizeId: int32(i), Quantity: 1})
		}
		_, err := ConvertPbTechCardMarkerInsertToEntity(with(entries...))
		require.Error(t, err)
		require.Contains(t, err.Error(), "max")
	})
}

// A piece tagged with a size the состав does not cut would resolve to zero instances under the
// instance formula: stored geometry, counted against the caps, never cut. Refused, not dropped.
func TestMarkerLayoutFactsRefuseAPieceSizeOutsideTheComposition(t *testing.T) {
	layout := &pb_common.TechCardMarkerLayout{
		SchemaVersion: 4,
		Composition:   []*pb_common.TechCardMarkerCompositionEntry{{SizeId: 3, Quantity: 1}},
		Pieces:        []*pb_common.TechCardMarkerPiece{{PieceId: 1, Quantity: 1, SizeId: 9}},
	}
	_, err := MarkerLayoutFactsFromPb(layout)
	require.Error(t, err)
	require.Contains(t, err.Error(), "which the состав does not cut")

	layout.Pieces[0].SizeId = 3
	facts, err := MarkerLayoutFactsFromPb(layout)
	require.NoError(t, err)
	require.True(t, facts.HasComposition)
	require.True(t, facts.HasPieceSize)

	// A size-agnostic piece (no размерный хвост in the DXF block) is legal and stays legal — it is
	// cut once per garment of the WHOLE состав.
	layout.Pieces[0].SizeId = 0
	facts, err = MarkerLayoutFactsFromPb(layout)
	require.NoError(t, err)
	require.False(t, facts.HasPieceSize)
}

// The blob may not carry Ф2 fields under a version that predates them — same rule, same reason as
// FlipPredatesSchema: a payload that lies about its own format is impossible, not legacy.
func TestCompositionPredatesSchema(t *testing.T) {
	require.True(t, entity.CompositionPredatesSchema(entity.MarkerLayoutFacts{SchemaVersion: 3, HasComposition: true}))
	require.True(t, entity.CompositionPredatesSchema(entity.MarkerLayoutFacts{SchemaVersion: 1, HasPieceSize: true}))
	require.False(t, entity.CompositionPredatesSchema(entity.MarkerLayoutFacts{SchemaVersion: 4, HasComposition: true}))
	require.False(t, entity.CompositionPredatesSchema(entity.MarkerLayoutFacts{SchemaVersion: 3}))
	// Ф1's constant must not move with the schema — that would hand the directional-cloth exemption
	// back to every v3 marker.
	require.Equal(t, 3, entity.MarkerLayoutSchemaWithFlip)
	require.Equal(t, 4, entity.MarkerLayoutSchemaWithComposition)
}

// consumption_per_unit_cm is emitted derived, never stored — the summary mapper divides.
func TestTechCardMarkerSummaryToPbDerivesConsumption(t *testing.T) {
	m := entity.TechCardMarkerSummary{
		Name:         "M · основная",
		SizeId:       sql.NullInt64{Int64: 3, Valid: true},
		Sets:         sql.NullInt64{Int64: 4, Valid: true},
		TotalUnits:   sql.NullInt64{Int64: 4, Valid: true},
		Composition:  []entity.MarkerCompositionEntry{{SizeId: 3, Quantity: 4}},
		UsedLengthCm: decimal.RequireFromString("512.4"),
	}
	pb := TechCardMarkerSummaryToPb(m)
	require.Equal(t, "128.1", pb.ConsumptionPerUnitCm.Value)
	require.Equal(t, int32(4), pb.TotalUnits)
	require.Empty(t, pb.ScalarApplyRefusal, "a homogeneous раскладка applies exactly as before")
	require.Len(t, pb.Composition, 1)

	// A DEPLOY-OVERLAP row: written by the old binary, so no children and no total_units. It still
	// reads as a состав из одного размера, and the divisor still cannot be zero.
	legacy := m
	legacy.Composition, legacy.TotalUnits = nil, sql.NullInt64{}
	pbLegacy := TechCardMarkerSummaryToPb(legacy)
	require.Equal(t, "128.1", pbLegacy.ConsumptionPerUnitCm.Value)
	require.Equal(t, int32(4), pbLegacy.TotalUnits)
	require.Len(t, pbLegacy.Composition, 1)

	// A row that lost its состав entirely — a Down of 0273, an ops delete of tech_card_marker_size, a
	// partial restore. The old `if Sets <= 0 { return UsedLengthCm }` guard reported the whole spread
	// as one garment's norm here; now the norm is WITHHELD and total_units says 0 rather than
	// claiming 1. Fail closed, because this is the shape the wire cannot tell from a real marker.
	broken := m
	broken.Composition, broken.TotalUnits, broken.Sets, broken.SizeId =
		nil, sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}
	pbBroken := TechCardMarkerSummaryToPb(broken)
	require.Nil(t, pbBroken.ConsumptionPerUnitCm, "a раскладка with no состав states no per-garment norm")
	require.Empty(t, pbBroken.Composition)
	require.Equal(t, int32(0), pbBroken.TotalUnits, "0 is «unknown»; 1 would be a claim this row does not make")
	require.NotEmpty(t, pbBroken.ScalarApplyRefusal)
	require.Contains(t, pbBroken.ScalarApplyRefusal, "нет состава")
}

// THE DEPLOY-OVERLAP INFLATION (C2). 0273 backfills total_units = sets, then the OLD binary's UPDATE
// writes `sets` and `used_length_cm` and knows neither total_units nor tech_card_marker_size. If the
// column were read first, the wire would carry the whole spread against a backfilled divisor of 1 —
// four times the truth — AND with an empty refusal, because one entry reads as homogeneous. The
// window is not minutes: a new container that fails its health check leaves the old deploy serving.
func TestLegacySetsBeatsAStaleTotalUnitsColumn(t *testing.T) {
	m := entity.TechCardMarkerSummary{
		Name:         "M · основная",
		SizeId:       sql.NullInt64{Int64: 3, Valid: true},
		Sets:         sql.NullInt64{Int64: 4, Valid: true},                      // the operator's re-save
		TotalUnits:   sql.NullInt64{Int64: 1, Valid: true},                      // what 0273 backfilled, now stale
		Composition:  []entity.MarkerCompositionEntry{{SizeId: 3, Quantity: 1}}, // stale projection
		UsedLengthCm: decimal.RequireFromString("1200"),
	}
	require.Equal(t, 4, m.TotalUnitsOrLegacy())
	require.Equal(t, []entity.MarkerCompositionEntry{{SizeId: 3, Quantity: 4}}, m.CompositionOrLegacy(),
		"while size_id is VALID the row is in the pre-Ф2 shape and (size_id, sets) is the fresher statement")
	pb := TechCardMarkerSummaryToPb(m)
	require.Equal(t, "300", pb.ConsumptionPerUnitCm.Value, "1200/4, not 1200/1")
	require.Equal(t, int32(4), pb.TotalUnits)

	// The symmetric case does not exist — no writer updates total_units without also writing `sets`
	// and the children — but once size_id goes NULL the row belongs to the Ф2 writer and the
	// projection is the only answer there is.
	mixed := entity.TechCardMarkerSummary{
		Name:         "смешанная",
		UsedLengthCm: decimal.RequireFromString("900"),
		TotalUnits:   sql.NullInt64{Int64: 3, Valid: true},
		Composition:  []entity.MarkerCompositionEntry{{SizeId: 3, Quantity: 1}, {SizeId: 4, Quantity: 2}},
	}
	require.Equal(t, 3, mixed.TotalUnitsOrLegacy())
	require.Len(t, mixed.CompositionOrLegacy(), 2)
}

// M2: the состав→piece direction. A состав line whose size lays out no piece charges the spread to
// garments the geometry never cuts, and total_units — the row AND tech_card_marker_size, which Ф4.5
// and Ф6.2 join as truth — records that overcount.
func TestMarkerLayoutFactsRefuseACompositionSizeWithNoPiece(t *testing.T) {
	layout := func(pieceSize int32) *pb_common.TechCardMarkerLayout {
		return &pb_common.TechCardMarkerLayout{
			SchemaVersion: 4,
			Composition: []*pb_common.TechCardMarkerCompositionEntry{
				{SizeId: 9, Quantity: 2}, {SizeId: 10, Quantity: 1},
			},
			Pieces: []*pb_common.TechCardMarkerPiece{{PieceId: 1, Quantity: 1, SizeId: pieceSize}},
		}
	}
	_, err := MarkerLayoutFactsFromPb(layout(9))
	require.Error(t, err)
	require.Contains(t, err.Error(), "no piece is laid out for it")

	// A blob where NO piece is graded is the legitimate «nothing in this DXF grades» case: every piece
	// is cut once per garment of the whole состав, so the check must not fire.
	_, err = MarkerLayoutFactsFromPb(layout(0))
	require.NoError(t, err)
}

// The quantity ceiling. Nothing ties placed_count/total_count to the состав, so without this a
// payload could declare two billion garments against six placements — buying not a big раскладка but
// consumption_per_unit_cm ≈ 0 with NO refusal (one entry reads as homogeneous), and a sum past 2^31
// that the INT column rejects as an unmapped Internal.
func TestMarkerCompositionQuantityCeiling(t *testing.T) {
	one := func(q int32) error {
		pb := validMarkerInsertPb()
		pb.Layout = &pb_common.TechCardMarkerLayout{
			SchemaVersion: 4,
			Composition:   []*pb_common.TechCardMarkerCompositionEntry{{SizeId: 3, Quantity: q}},
		}
		_, err := ConvertPbTechCardMarkerInsertToEntity(pb)
		return err
	}
	require.NoError(t, one(entity.MaxMarkerTotalUnits))
	require.ErrorContains(t, one(entity.MaxMarkerTotalUnits+1), "max")
	require.ErrorContains(t, one(1<<30), "max")

	// The SUM is capped too: no garment can be cut without at least one placement, and placements stop
	// at maxMarkerPlacements.
	pb := validMarkerInsertPb()
	pb.Layout = &pb_common.TechCardMarkerLayout{
		SchemaVersion: 4,
		Composition: []*pb_common.TechCardMarkerCompositionEntry{
			{SizeId: 3, Quantity: entity.MaxMarkerTotalUnits}, {SizeId: 4, Quantity: 1},
		},
	}
	_, err := ConvertPbTechCardMarkerInsertToEntity(pb)
	require.ErrorContains(t, err, "garments, max")
}

// Р2: a MIXED состав does not hand out a scalar norm at all. Withheld, not labelled — the number
// would be copied into tech_card_colorway_usage.consumption as a persistent fact and frozen into the
// release snapshot, and a caption travels with neither.
func TestMixedCompositionWithholdsTheScalarNorm(t *testing.T) {
	m := entity.TechCardMarkerSummary{
		Name:         "смешанная",
		UsedLengthCm: decimal.RequireFromString("900"),
		TotalUnits:   sql.NullInt64{Int64: 4, Valid: true},
		Composition: []entity.MarkerCompositionEntry{
			{SizeId: 3, Quantity: 1}, {SizeId: 4, Quantity: 2}, {SizeId: 5, Quantity: 1},
		},
	}
	pb := TechCardMarkerSummaryToPb(m)
	require.Nil(t, pb.ConsumptionPerUnitCm, "the mean across a состав must not reach a recipe")
	require.NotEmpty(t, pb.ScalarApplyRefusal)
	require.Contains(t, pb.ScalarApplyRefusal, "Ф2.4", "the refusal must name what closes the gap")
	require.Contains(t, pb.ScalarApplyRefusal, "одного размера", "and offer an action")
	// The facts stay on the wire: the раскладка is still readable, measurable and displayable.
	require.Equal(t, int32(4), pb.TotalUnits)
	require.Len(t, pb.Composition, 3)
	require.Equal(t, "900", pb.UsedLengthCm.Value)
	require.Equal(t, int32(0), pb.SizeId)
	require.Equal(t, int32(0), pb.Sets)
}

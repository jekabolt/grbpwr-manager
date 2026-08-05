package dto

import (
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
		require.Equal(t, 4, out.Sets)
		require.True(t, out.EfficiencyPct.Valid)
		require.Equal(t, "512.4", out.UsedLengthCm.String())
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
		{"missing size", func(pb *pb_common.TechCardMarkerInsert) { pb.SizeId = 0 }, "size_id"},
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
		{"zero sets", func(pb *pb_common.TechCardMarkerInsert) { pb.Sets = 0 }, "sets"},
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

// consumption_per_unit_cm is emitted derived, never stored — the summary mapper divides.
func TestTechCardMarkerSummaryToPbDerivesConsumption(t *testing.T) {
	m := entity.TechCardMarkerSummary{
		Sets:         4,
		UsedLengthCm: decimal.RequireFromString("512.4"),
	}
	pb := TechCardMarkerSummaryToPb(m)
	require.Equal(t, "128.1", pb.ConsumptionPerUnitCm.Value)

	// A defensive zero (the CHECK forbids it in the DB) must not divide by zero.
	m.Sets = 0
	require.Equal(t, "512.4", TechCardMarkerSummaryToPb(m).ConsumptionPerUnitCm.Value)
}

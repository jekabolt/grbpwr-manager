package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

func TestNormalizeGarmentLabelStitchLines(t *testing.T) {
	h100 := nd("100")
	for _, tc := range []struct {
		name   string
		raw    string
		height decimal.NullDecimal
		want   string
	}{
		{"empty", "", h100, ""},
		{"blank", "  ", h100, ""},
		{"top edge allowed", "0", h100, "0"},
		{"bottom edge allowed", "100", h100, "100"},
		{"both edges and middle sorted", "100, 50,0", h100, "0,50,100"},
		{"past the height dropped", "100.04,150,99.9", h100, "99.9,100"},
		{"negative and junk dropped", "-1,abc,,0.04,12", h100, "0,12"},
		{"rounded and deduped", "12.04,12.05,12.0,33.333", h100, "12,12.1,33.3"},
		{"no height keeps every non-negative", "150,0,-3", decimal.NullDecimal{}, "0,150"},
		{"absurd value dropped without height", "100000,5", decimal.NullDecimal{}, "5"},
		{"at most eight, the first eight", "9,8,7,6,5,4,3,2,1,0", h100, "0,1,2,3,4,5,6,7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, NormalizeGarmentLabelStitchLines(tc.raw, tc.height))
		})
	}
}

func TestGarmentLabelStitchLinesPresence(t *testing.T) {
	present := ""
	lines := "100, 0,200"
	labels, err := parseTechCardGarmentLabels([]*pb_common.TechCardGarmentLabel{
		{Key: "size"},
		{Key: "brand", StitchLinesMm: &present},
		{Key: "care", HeightMm: &pb_decimal.Decimal{Value: "100"}, StitchLinesMm: &lines},
	})
	require.NoError(t, err)
	require.True(t, labels[0].StitchLinesOmitted)
	require.False(t, labels[1].StitchLinesOmitted)
	require.Equal(t, "", labels[1].StitchLinesMm)
	require.False(t, labels[2].StitchLinesOmitted)
	require.Equal(t, "0,100", labels[2].StitchLinesMm, "the edges stay, 200 lies past the height")

	pb := techCardGarmentLabelsToPb([]entity.TechCardGarmentLabel{{Key: "size"}, {Key: "care", StitchLinesMm: "0,40"}})
	require.NotNil(t, pb[0].StitchLinesMm)
	require.Equal(t, "", pb[0].GetStitchLinesMm())
	require.Equal(t, "0,40", pb[1].GetStitchLinesMm())
}

func TestCarryOmittedGarmentLabelStitchLines(t *testing.T) {
	stored := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{
		{Key: "size", LineKey: labelKeyA, HeightMm: nd("100"), FoldLinesMm: "50", StitchLinesMm: "0,80"},
		{Key: "brand", LineKey: labelKeyB, StitchLinesMm: "30"},
	}}
	in := &entity.TechCardInsert{LabelsAware: true, GarmentLabels: []entity.TechCardGarmentLabel{
		// Knows the fold lines, not the stitch lines; shortened the label.
		{Key: "size", LineKey: labelKeyA, HeightMm: nd("60"), FoldLinesMm: "20", StitchLinesOmitted: true},
		{Key: "brand", LineKey: labelKeyB, StitchLinesMm: ""},
	}}
	CarryStoredGarmentLabelGeometry(stored, in)
	require.Equal(t, "0", in.GarmentLabels[0].StitchLinesMm, "carried and cut to the new height")
	require.Equal(t, "20", in.GarmentLabels[0].FoldLinesMm, "the fold lines the client sent stay")
	require.Equal(t, "", in.GarmentLabels[1].StitchLinesMm, "an explicit empty list wins")

	unaware := &entity.TechCardInsert{}
	CarryStoredGarmentLabelGeometry(stored, unaware)
	require.Equal(t, "0,80", unaware.GarmentLabels[0].StitchLinesMm)
}

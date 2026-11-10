package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

func TestNormalizeGarmentLabelFoldAxisAndPages(t *testing.T) {
	for in, want := range map[string]string{"": "", "width": "width", " Width ": "width", "WIDTH": "width", "height": "", "left": ""} {
		require.Equal(t, want, NormalizeGarmentLabelFoldAxis(in), in)
	}
	for in, want := range map[int]int{-5: 1, 0: 1, 1: 1, 7: 7, 12: 12, 13: 12, 900: 12} {
		require.Equal(t, want, NormalizeGarmentLabelPages(in), "pages %d", in)
	}
}

func TestGarmentLabelAxisPagesParse(t *testing.T) {
	axis, wide := "width", "Width "
	pages, tooMany, none := int32(5), int32(40), int32(-2)
	lines := "10,50,90,150"
	labels, err := parseTechCardGarmentLabels([]*pb_common.TechCardGarmentLabel{
		{Key: "a"},
		{Key: "b", FoldAxis: &axis, Pages: &pages, WidthMm: &pb_decimal.Decimal{Value: "100"},
			HeightMm: &pb_decimal.Decimal{Value: "30"}, FoldLinesMm: &lines, StitchLinesMm: &lines},
		{Key: "c", FoldAxis: &wide, Pages: &tooMany},
		{Key: "d", Pages: &none},
		{Key: "e", HeightMm: &pb_decimal.Decimal{Value: "30"}, WidthMm: &pb_decimal.Decimal{Value: "100"},
			FoldAxis: new(string), FoldLinesMm: &lines},
	})
	require.NoError(t, err)
	require.True(t, labels[0].FoldAxisOmitted)
	require.True(t, labels[0].PagesOmitted)
	require.Equal(t, 1, labels[0].Pages)
	require.False(t, labels[1].FoldAxisOmitted)
	require.Equal(t, "width", labels[1].FoldAxis)
	require.Equal(t, 5, labels[1].Pages)
	require.Equal(t, "10,50,90", labels[1].FoldLinesMm, "axis=width: range against the 100 mm width, not the 30 mm height")
	require.Equal(t, "10,50,90", labels[1].StitchLinesMm)
	require.Equal(t, "width", labels[2].FoldAxis)
	require.Equal(t, 12, labels[2].Pages)
	require.Equal(t, 1, labels[3].Pages)
	require.False(t, labels[3].PagesOmitted)
	require.Equal(t, "10", labels[4].FoldLinesMm, "axis \"\": range against the 30 mm height")

	pb := techCardGarmentLabelsToPb([]entity.TechCardGarmentLabel{{Key: "a"}, {Key: "b", FoldAxis: "width", Pages: 4}})
	require.NotNil(t, pb[0].FoldAxis)
	require.Equal(t, "", pb[0].GetFoldAxis())
	require.EqualValues(t, 1, pb[0].GetPages())
	require.Equal(t, "width", pb[1].GetFoldAxis())
	require.EqualValues(t, 4, pb[1].GetPages())
}

func TestCarryOmittedGarmentLabelAxisAndPages(t *testing.T) {
	stored := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{
		{Key: "size", LineKey: labelKeyA, WidthMm: nd("100"), HeightMm: nd("30"), FoldAxis: "width", Pages: 6,
			FoldLinesMm: "40,80", StitchLinesMm: "0,100"},
		{Key: "brand", LineKey: labelKeyB, HeightMm: nd("30"), Pages: 3},
	}}
	in := &entity.TechCardInsert{LabelsAware: true, GarmentLabels: []entity.TechCardGarmentLabel{
		{Key: "size", LineKey: labelKeyA, WidthMm: nd("60"), HeightMm: nd("30"), FoldAxisOmitted: true,
			PagesOmitted: true, FoldLinesOmitted: true, StitchLinesOmitted: true},
		{Key: "brand", LineKey: labelKeyB, WidthMm: nd("100"), HeightMm: nd("30"), FoldAxis: "width", FoldLinesMm: "50,90",
			PagesOmitted: true},
	}}
	CarryStoredGarmentLabelGeometry(stored, in)
	a, b := in.GarmentLabels[0], in.GarmentLabels[1]
	require.Equal(t, "width", a.FoldAxis)
	require.Equal(t, 6, a.Pages)
	require.Equal(t, "40", a.FoldLinesMm, "cut to the new 60 mm width, 80 is gone")
	require.Equal(t, "0", a.StitchLinesMm, "100 lies past the width, 0 is the sewn edge")
	require.Equal(t, "width", b.FoldAxis)
	require.Equal(t, 3, b.Pages)
	require.Equal(t, "50,90", b.FoldLinesMm)

	unaware := &entity.TechCardInsert{}
	CarryStoredGarmentLabelGeometry(stored, unaware)
	require.Equal(t, "width", unaware.GarmentLabels[0].FoldAxis)
	require.Equal(t, 6, unaware.GarmentLabels[0].Pages)
}

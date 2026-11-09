package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

func TestNormalizeGarmentLabelFoldLines(t *testing.T) {
	h100 := nd("100")
	for _, tc := range []struct {
		name   string
		raw    string
		height decimal.NullDecimal
		want   string
	}{
		{"empty", "", h100, ""},
		{"blank", "  ", h100, ""},
		{"one line", "50", h100, "50"},
		{"sorted and trimmed", " 88 , 12 ", h100, "12,88"},
		{"rounded to 0.1", "12.04,12.05,33.333", h100, "12,12.1,33.3"},
		{"duplicates after rounding", "12,12.0,12.01,12", h100, "12"},
		{"bad items dropped", "abc,,12,NaN,1e,-5,0,0.04", h100, "12"},
		{"height is exclusive", "99.9,100,100.04,150", h100, "99.9"},
		{"no height keeps every positive", "150,0.1", decimal.NullDecimal{}, "0.1,150"},
		{"absurd value dropped without height", "100000,5", decimal.NullDecimal{}, "5"},
		{"at most eight, the first eight", "9,8,7,6,5,4,3,2,1", h100, "1,2,3,4,5,6,7,8"},
		{"trailing zeros stripped", "12.50", h100, "12.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, NormalizeGarmentLabelFoldLines(tc.raw, tc.height))
		})
	}
}

func TestGarmentLabelFoldLinesPresence(t *testing.T) {
	present := ""
	lines := "88, 12,200"
	labels, err := parseTechCardGarmentLabels([]*pb_common.TechCardGarmentLabel{
		{Key: "size"},
		{Key: "brand", FoldLinesMm: &present},
		{Key: "care", HeightMm: &pb_decimal.Decimal{Value: "100"}, FoldLinesMm: &lines},
	})
	require.NoError(t, err, "bad items never fail a save")
	require.True(t, labels[0].FoldLinesOmitted)
	require.False(t, labels[1].FoldLinesOmitted)
	require.Equal(t, "", labels[1].FoldLinesMm)
	require.False(t, labels[2].FoldLinesOmitted)
	require.Equal(t, "12,88", labels[2].FoldLinesMm, "200 lies past the height and is dropped")

	pb := techCardGarmentLabelsToPb([]entity.TechCardGarmentLabel{{Key: "size"}, {Key: "care", FoldLinesMm: "12,88"}})
	require.NotNil(t, pb[0].FoldLinesMm, "the read always sends the field, so a round trip never reads as an older client")
	require.Equal(t, "", pb[0].GetFoldLinesMm())
	require.Equal(t, "12,88", pb[1].GetFoldLinesMm())
}

func TestCarryOmittedGarmentLabelFoldLines(t *testing.T) {
	stored := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{
		{Key: "size", LineKey: labelKeyA, HeightMm: nd("100"), FoldLinesMm: "12,88"},
		{Key: "brand", LineKey: labelKeyB, FoldLinesMm: "50"},
	}}
	in := &entity.TechCardInsert{LabelsAware: true, GarmentLabels: []entity.TechCardGarmentLabel{
		// A 0454 client: knows the line key, not the lines, and shortened the label.
		{Key: "size", LineKey: labelKeyA, HeightMm: nd("60"), FoldLinesOmitted: true},
		// A current client that cleared the lines.
		{Key: "brand", LineKey: labelKeyB, FoldLinesMm: ""},
	}}
	CarryStoredGarmentLabelGeometry(stored, in)
	require.Equal(t, "12", in.GarmentLabels[0].FoldLinesMm, "stored lines are carried and cut to the new height")
	require.Equal(t, "", in.GarmentLabels[1].FoldLinesMm, "an explicit empty list wins over the stored one")

	// A pre-0454 client sends neither the line key nor the lines: the unique label-key match carries both.
	old := &entity.TechCardInsert{LabelsAware: true, GarmentLabels: []entity.TechCardGarmentLabel{
		{Key: " Size ", FoldLinesOmitted: true},
	}}
	CarryStoredGarmentLabelGeometry(stored, old)
	require.Equal(t, labelKeyA, old.GarmentLabels[0].LineKey)
	require.Equal(t, "12,88", old.GarmentLabels[0].FoldLinesMm)

	// A labels-unaware client keeps the stored rows whole.
	unaware := &entity.TechCardInsert{}
	CarryStoredGarmentLabelGeometry(stored, unaware)
	require.Equal(t, "12,88", unaware.GarmentLabels[0].FoldLinesMm)
}

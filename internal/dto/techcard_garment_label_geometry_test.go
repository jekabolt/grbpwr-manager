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

const (
	labelKeyA = "01JABCDEFGHJKMNPQRSTVWXYZ1"
	labelKeyB = "01JABCDEFGHJKMNPQRSTVWXYZ2"
)

func TestGarmentLabelGeometryValidation(t *testing.T) {
	d := func(value string) *pb_decimal.Decimal { return &pb_decimal.Decimal{Value: value} }
	cases := []struct {
		name  string
		label *pb_common.TechCardGarmentLabel
		field string
	}{
		{"invalid fold", &pb_common.TechCardGarmentLabel{Key: "size", Fold: "center"}, "garment_labels[0].fold"},
		{"zero width", &pb_common.TechCardGarmentLabel{Key: "size", WidthMm: d("0")}, "garment_labels[0].width_mm"},
		{"negative height", &pb_common.TechCardGarmentLabel{Key: "size", HeightMm: d("-1")}, "garment_labels[0].height_mm"},
		{"width too large", &pb_common.TechCardGarmentLabel{Key: "size", WidthMm: d("100000")}, "garment_labels[0].width_mm"},
		{"height too precise", &pb_common.TechCardGarmentLabel{Key: "size", HeightMm: d("1.001")}, "garment_labels[0].height_mm"},
		{"bad decimal", &pb_common.TechCardGarmentLabel{Key: "size", WidthMm: d("wide")}, "garment_labels[0].width_mm"},
		{"short key", &pb_common.TechCardGarmentLabel{Key: "size", LineKey: "SHORT"}, "garment_labels[0].line_key"},
		{"key punctuation", &pb_common.TechCardGarmentLabel{Key: "size", LineKey: "01JABCDEFGHJKMNPQRSTVWXY-1"}, "garment_labels[0].line_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseTechCardGarmentLabels([]*pb_common.TechCardGarmentLabel{tc.label})
			requireLabelFieldViolation(t, err, tc.field)
		})
	}

	_, err := parseTechCardGarmentLabels([]*pb_common.TechCardGarmentLabel{
		{Key: "brand", LineKey: labelKeyA}, {Key: "size", LineKey: strings.ToLower(labelKeyA)},
	})
	requireLabelFieldViolation(t, err, "garment_labels[1].line_key")
}

func TestGarmentLabelGeometryNormalisesAndKeepsMaximum(t *testing.T) {
	labels, err := parseTechCardGarmentLabels([]*pb_common.TechCardGarmentLabel{{
		Key: "size", LineKey: strings.ToLower(labelKeyA), WidthMm: &pb_decimal.Decimal{Value: "99999.99"},
		HeightMm: &pb_decimal.Decimal{Value: "0.01"}, Fold: "half",
	}})
	require.NoError(t, err)
	require.Equal(t, labelKeyA, labels[0].LineKey)
	require.Equal(t, decimal.RequireFromString("99999.99"), labels[0].WidthMm.Decimal)
	require.Equal(t, decimal.RequireFromString("0.01"), labels[0].HeightMm.Decimal)
	require.Equal(t, sql.NullString{String: "half", Valid: true}, labels[0].Fold)
}

func TestFinalizeGarmentLabelGeometryMintsAndValidatesLinks(t *testing.T) {
	in := &entity.TechCardInsert{
		GarmentLabels: []entity.TechCardGarmentLabel{{Key: "size"}, {Key: "brand", LineKey: strings.ToLower(labelKeyB)}},
		Operations:    []entity.TechCardOperation{{LabelKeys: []string{strings.ToLower(labelKeyB)}}},
	}
	require.NoError(t, finalizeGarmentLabelGeometry(in))
	require.Len(t, in.GarmentLabels[0].LineKey, 26)
	require.Equal(t, labelKeyB, in.GarmentLabels[1].LineKey)
	require.Equal(t, []string{labelKeyB}, in.Operations[0].LabelKeys)

	missing := &entity.TechCardInsert{
		GarmentLabels: []entity.TechCardGarmentLabel{{Key: "keyless"}},
		Operations:    []entity.TechCardOperation{{LabelKeys: []string{labelKeyA}}},
	}
	err := finalizeGarmentLabelGeometry(missing)
	requireLabelFieldViolation(t, err, "operations[0].label_keys[0]")
	require.Len(t, missing.GarmentLabels[0].LineKey, 26, "membership is checked after minting")

	collision := &entity.TechCardInsert{
		Pieces:        []entity.TechCardPiece{{LineKey: labelKeyA}},
		GarmentLabels: []entity.TechCardGarmentLabel{{Key: "size", LineKey: labelKeyA}},
	}
	err = finalizeGarmentLabelGeometry(collision)
	requireLabelFieldViolation(t, err, "garment_labels[0].line_key")
}

func TestOperationLabelKeysCollapseDuplicatesAndStayOutOfDigests(t *testing.T) {
	got, err := operationLabelKeysFromPb(2, []string{strings.ToLower(labelKeyA), labelKeyA, " ", labelKeyB})
	require.NoError(t, err)
	require.Equal(t, []string{labelKeyA, labelKeyB}, got)

	_, err = operationLabelKeysFromPb(2, []string{"short"})
	requireLabelFieldViolation(t, err, "operations[2].label_keys[0]")

	base := &entity.TechCardInsert{Operations: []entity.TechCardOperation{{}}}
	linked := &entity.TechCardInsert{Operations: []entity.TechCardOperation{{LabelKeys: []string{labelKeyA}}}}
	require.Equal(t, TechCardSectionDigests(base), TechCardSectionDigests(linked))
	require.Equal(t, []string{labelKeyA}, techCardOperationsToPb(linked.Operations)[0].GetLabelKeys())
}

func TestCarryStoredGarmentLabelGeometryAndOperationLinks(t *testing.T) {
	stored := &entity.TechCardInsert{
		GarmentLabels: []entity.TechCardGarmentLabel{
			{Key: " Size ", LineKey: labelKeyA, WidthMm: nd("40"), HeightMm: nd("15"), Fold: sql.NullString{String: "half", Valid: true}},
			{Key: "duplicate", LineKey: labelKeyB},
			{Key: " DUPLICATE ", LineKey: "01JABCDEFGHJKMNPQRSTVWXYZ3"},
		},
		Operations: []entity.TechCardOperation{{OperationNumber: sql.NullInt32{Int32: 10, Valid: true}, LabelKeys: []string{labelKeyA, labelKeyB}}},
	}
	in := &entity.TechCardInsert{
		LabelsAware:   true,
		GarmentLabels: []entity.TechCardGarmentLabel{{Key: "size"}, {Key: "duplicate"}},
		Operations:    []entity.TechCardOperation{{OperationNumber: sql.NullInt32{Int32: 10, Valid: true}}},
	}
	CarryStoredGarmentLabelGeometry(stored, in)
	require.Equal(t, labelKeyA, in.GarmentLabels[0].LineKey)
	require.Equal(t, nd("40"), in.GarmentLabels[0].WidthMm)
	require.Equal(t, nd("15"), in.GarmentLabels[0].HeightMm)
	require.Equal(t, "half", in.GarmentLabels[0].Fold.String)
	require.Empty(t, in.GarmentLabels[1].LineKey, "an ambiguous human key does not carry geometry")
	require.Equal(t, []string{labelKeyA}, in.Operations[0].LabelKeys, "links to labels no longer in the payload are dropped")
}

func TestCarryStoredGarmentLabelsForLabelsUnawareClient(t *testing.T) {
	stored := &entity.TechCardInsert{
		GarmentLabels: []entity.TechCardGarmentLabel{{Key: "size", LineKey: labelKeyA, MediaIds: []int{4}}},
		Operations:    []entity.TechCardOperation{{OperationNumber: sql.NullInt32{Int32: 10, Valid: true}, LabelKeys: []string{labelKeyA}}},
	}
	in := &entity.TechCardInsert{Operations: []entity.TechCardOperation{{OperationNumber: sql.NullInt32{Int32: 10, Valid: true}}}}
	CarryStoredGarmentLabelGeometry(stored, in)
	require.Equal(t, labelKeyA, in.GarmentLabels[0].LineKey)
	require.Equal(t, []int{4}, in.GarmentLabels[0].MediaIds)
	require.Equal(t, []string{labelKeyA}, in.Operations[0].LabelKeys)
	in.GarmentLabels[0].MediaIds[0] = 9
	require.Equal(t, 4, stored.GarmentLabels[0].MediaIds[0], "the carried list is not aliased")
}

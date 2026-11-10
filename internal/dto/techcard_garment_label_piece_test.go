package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

func mm(v string) *pb_decimal.Decimal { return &pb_decimal.Decimal{Value: v} }

func TestNormalizeGarmentLabelOnPiece(t *testing.T) {
	for in, want := range map[string]string{"": "", "4_sides": "4_sides", " TOP_EDGE ": "top_edge", "top": "", "sides": ""} {
		require.Equal(t, want, NormalizeGarmentLabelOnPiece(in), in)
	}
}

func TestParseGarmentLabelMm(t *testing.T) {
	v, err := parseGarmentLabelMm("f", nil, maxPieceMm)
	require.NoError(t, err)
	require.True(t, v.IsZero())
	v, err = parseGarmentLabelMm("f", mm("12.50"), maxPieceMm)
	require.NoError(t, err)
	require.True(t, v.Equal(decimal.RequireFromString("12.5")))
	for _, bad := range []string{"-1", "abc", "1.234", "100000"} {
		_, err = parseGarmentLabelMm("f", mm(bad), maxPieceMm)
		require.Error(t, err, bad)
	}
	_, err = parseGarmentLabelMm("f", mm("1000"), maxSeamMm)
	require.Error(t, err)
}

func TestGarmentLabelPieceParsePresenceAndPrints(t *testing.T) {
	on, junk := "4_sides", "diagonal"
	bom := int32(7)
	labels, err := parseTechCardGarmentLabels([]*pb_common.TechCardGarmentLabel{
		{Key: "old"},
		{Key: "new", PieceBomItemId: &bom, PieceWidthMm: mm("60"), PieceHeightMm: mm("40"), PieceSeamMm: mm("10"),
			LabelXMm: mm("5"), LabelYMm: mm("12"), LabelOnPiece: &on,
			Prints: []*pb_common.TechCardGarmentLabelPrint{
				{Name: " logo ", LibraryFileId: 3, Method: "Screen", WidthMm: mm("10"), HeightMm: mm("5"), XMm: mm("1"), YMm: mm("0")},
				{Method: "woven", WidthMm: mm("2"), HeightMm: mm("2")},
			}},
		{Key: "junk", LabelOnPiece: &junk},
	})
	require.NoError(t, err)
	require.True(t, labels[0].PieceOmitted.All())
	require.True(t, labels[0].PrintsOmitted)
	n := labels[1]
	require.False(t, n.PieceOmitted.Any())
	require.False(t, n.PrintsOmitted)
	require.Equal(t, sql.NullInt32{Int32: 7, Valid: true}, n.PieceBomItemId)
	require.Equal(t, "4_sides", n.LabelOnPiece)
	require.Len(t, n.Prints, 2)
	require.Equal(t, "screen", n.Prints[0].Method)
	require.Equal(t, "logo", n.Prints[0].Name)
	require.EqualValues(t, 3, n.Prints[0].LibraryFileId.Int32)
	require.False(t, n.Prints[1].LibraryFileId.Valid, "0 = no file")
	require.Equal(t, "", labels[2].LabelOnPiece, "unknown value normalises to empty")
	require.False(t, labels[2].PrintsOmitted)

	// Round trip to the wire keeps every field.
	pb := techCardGarmentLabelsToPb(labels)
	require.Equal(t, "4_sides", pb[1].GetLabelOnPiece())
	require.Equal(t, int32(7), pb[1].GetPieceBomItemId())
	require.Equal(t, "12", pb[1].GetLabelYMm().GetValue())
	require.Len(t, pb[1].Prints, 2)
	require.Equal(t, int32(3), pb[1].Prints[0].LibraryFileId)
}

func TestGarmentLabelPrintParseRejects(t *testing.T) {
	cases := map[string]*pb_common.TechCardGarmentLabelPrint{
		"method":   {Method: "laser", WidthMm: mm("1"), HeightMm: mm("1")},
		"zero w":   {Method: "screen", WidthMm: mm("0"), HeightMm: mm("1")},
		"no h":     {Method: "screen", WidthMm: mm("1")},
		"neg x":    {Method: "screen", WidthMm: mm("1"), HeightMm: mm("1"), XMm: mm("-1")},
		"neg file": {Method: "screen", WidthMm: mm("1"), HeightMm: mm("1"), LibraryFileId: -2},
	}
	for name, p := range cases {
		on := ""
		_, err := parseTechCardGarmentLabels([]*pb_common.TechCardGarmentLabel{{Key: "a", LabelOnPiece: &on,
			Prints: []*pb_common.TechCardGarmentLabelPrint{p}}})
		require.Error(t, err, name)
	}
	neg := mm("-3")
	_, err := parseTechCardGarmentLabels([]*pb_common.TechCardGarmentLabel{{Key: "a", PieceWidthMm: neg}})
	require.Error(t, err, "negative piece size")
}

func TestValidateGarmentLabelPieceFit(t *testing.T) {
	d := decimal.RequireFromString
	base := func() entity.TechCardGarmentLabel {
		return entity.TechCardGarmentLabel{
			WidthMm: decimal.NewNullDecimal(d("40")), HeightMm: decimal.NewNullDecimal(d("20")),
			PieceWidthMm: d("60"), PieceHeightMm: d("40"), PieceSeamMm: d("10"), LabelXMm: d("10"), LabelYMm: d("10"),
		}
	}
	ok := base()
	require.NoError(t, ValidateGarmentLabelPiece("l", &ok))
	exact := base()
	exact.LabelXMm, exact.LabelYMm = d("20"), d("20")
	require.NoError(t, ValidateGarmentLabelPiece("l", &exact), "touching the edges fits")
	noPiece := base()
	noPiece.PieceWidthMm, noPiece.LabelXMm = decimal.Zero, d("500")
	require.NoError(t, ValidateGarmentLabelPiece("l", &noPiece), "no piece, no fit check")

	for name, mut := range map[string]func(*entity.TechCardGarmentLabel){
		"seam past piece": func(l *entity.TechCardGarmentLabel) { l.PieceSeamMm = d("40") },
		"in seam":         func(l *entity.TechCardGarmentLabel) { l.LabelYMm = d("9.99") },
		"too wide":        func(l *entity.TechCardGarmentLabel) { l.LabelXMm = d("20.01") },
		"too tall":        func(l *entity.TechCardGarmentLabel) { l.LabelYMm = d("20.5") },
	} {
		l := base()
		mut(&l)
		require.Error(t, ValidateGarmentLabelPiece("l", &l), name)
	}
}

func TestValidateGarmentLabelPrintsInsideLabel(t *testing.T) {
	d := decimal.RequireFromString
	label := func(prints ...entity.TechCardGarmentLabelPrint) *entity.TechCardInsert {
		return &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{{
			WidthMm: decimal.NewNullDecimal(d("40")), HeightMm: decimal.NewNullDecimal(d("20")), Prints: prints,
		}}}
	}
	p := func(x, y, w, h string) entity.TechCardGarmentLabelPrint {
		return entity.TechCardGarmentLabelPrint{Method: "screen", XMm: d(x), YMm: d(y), WidthMm: d(w), HeightMm: d(h)}
	}
	require.NoError(t, validateGarmentLabelPieceAndPrints(label(p("0", "0", "40", "20"))))
	require.Error(t, validateGarmentLabelPieceAndPrints(label(p("1", "0", "40", "20"))))
	require.Error(t, validateGarmentLabelPieceAndPrints(label(p("0", "15", "5", "6"))))
	unsized := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{{Prints: []entity.TechCardGarmentLabelPrint{p("0", "0", "1", "1")}}}}
	require.Error(t, validateGarmentLabelPieceAndPrints(unsized), "prints need a label size")
	unsized.GarmentLabels[0].PrintsOmitted = true
	require.NoError(t, validateGarmentLabelPieceAndPrints(unsized), "carried prints are not judged")
}

func TestCarryOmittedGarmentLabelPiece(t *testing.T) {
	d := decimal.RequireFromString
	const key = "01JABCDEFGHJKMNPQRSTVWXYZ1"
	stored := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{{
		LineKey: key, PieceBomLineKey: "fab", PieceWidthMm: d("60"), PieceHeightMm: d("40"), LabelOnPiece: "top_edge",
		Prints: []entity.TechCardGarmentLabelPrint{{Name: "logo"}},
	}}}
	in := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{{
		LineKey: key, PieceWidthMm: d("70"),
		PieceOmitted:  entity.GarmentLabelPieceOmitted{BomItem: true, Height: true, Seam: true, LabelX: true, LabelY: true, LabelOnPiece: true},
		PrintsOmitted: true,
	}}}
	carryOmittedGarmentLabelPiece(stored, in)
	got := in.GarmentLabels[0]
	require.True(t, got.PieceWidthMm.Equal(d("70")), "a sent field wins")
	require.True(t, got.PieceHeightMm.Equal(d("40")))
	require.Equal(t, "fab", got.PieceBomLineKey)
	require.Equal(t, "top_edge", got.LabelOnPiece)
	require.Len(t, got.Prints, 1)

	sent := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{{LineKey: key, LabelOnPiece: ""}}}
	carryOmittedGarmentLabelPiece(stored, sent)
	require.Empty(t, sent.GarmentLabels[0].Prints, "a sent empty list replaces the stored prints")
}

package entity

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

const (
	topTestPiece = "01JABCDEFGHJKMNPQRSTVWXYZ1"
	topTestLine  = "01JABCDEFGHJKMNPQRSTVWXYT1"
	topTestLine2 = "01JABCDEFGHJKMNPQRSTVWXYT2"
)

func topTestCard() *TechCardInsert {
	return &TechCardInsert{
		SizeIds: []int{1, 2},
		Pieces:  []TechCardPiece{{LineKey: topTestPiece, Name: "front", PiecesPerGarment: 1}},
		BomItems: []TechCardBomItem{{
			Id: 91, LineKey: cutTestLine, Section: BomSectionThread, Name: "thread",
			Unit: sql.NullString{String: "m", Valid: true}, ThreadPerMm: decimal.NewNullDecimal(cutDec("2.5")),
		}},
		Operations: []TechCardOperation{{
			BomLineKeys: []string{cutTestLine},
			TopstitchLines: []TechCardTopstitchLine{{
				LineKey: topTestLine, BomLineKey: cutTestLine,
				Parts: []TechCardSeamAnchor{seamTestAnchor(topTestPiece, "FR#1")}, Rows: 2,
				Sizes: []TechCardTopstitchLineSize{
					{SizeId: 2, LineMm: decimal.NewNullDecimal(cutDec("220"))},
					{SizeId: 1, LineMm: decimal.NewNullDecimal(cutDec("200"))},
				},
			}},
		}},
	}
}

func TestValidateTechCardTopstitchLinesNormalises(t *testing.T) {
	in := topTestCard()
	in.Operations[0].TopstitchLines[0].LineKey = strings.ToLower(topTestLine)
	require.NoError(t, ValidateTechCardTopstitchLines(in))
	line := in.Operations[0].TopstitchLines[0]
	require.Equal(t, topTestLine, line.LineKey)
	require.Equal(t, []int{1, 2}, []int{line.Sizes[0].SizeId, line.Sizes[1].SizeId})
}

func TestValidateTechCardTopstitchLinesRefusals(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*TechCardInsert)
		field string
	}{
		{"thread zero", func(c *TechCardInsert) { c.BomItems[0].ThreadPerMm = decimal.NewNullDecimal(decimal.Zero) }, "bom_items[0].thread_per_mm"},
		{"thread over cap", func(c *TechCardInsert) { c.BomItems[0].ThreadPerMm = decimal.NewNullDecimal(cutDec("50.001")) }, "bom_items[0].thread_per_mm"},
		{"bad key", func(c *TechCardInsert) { c.Operations[0].TopstitchLines[0].LineKey = "short" }, "operations[0].topstitch_lines[0].line_key"},
		{"shared piece key", func(c *TechCardInsert) {
			c.Operations[0].TopstitchLines[0].LineKey = topTestPiece
		}, "operations[0].topstitch_lines[0].line_key"},
		{"shared strip key", func(c *TechCardInsert) {
			c.CutStrips = []TechCardCutStrip{{LineKey: topTestLine}}
		}, "operations[0].topstitch_lines[0].line_key"},
		{"shared label key", func(c *TechCardInsert) {
			c.GarmentLabels = []TechCardGarmentLabel{{LineKey: topTestLine}}
		}, "operations[0].topstitch_lines[0].line_key"},
		{"duplicate line", func(c *TechCardInsert) {
			c.Operations[0].TopstitchLines = append(c.Operations[0].TopstitchLines, c.Operations[0].TopstitchLines[0])
		}, "operations[0].topstitch_lines[1].line_key"},
		{"no bom", func(c *TechCardInsert) { c.Operations[0].TopstitchLines[0].BomLineKey = "" }, "operations[0].topstitch_lines[0].bom_line_key"},
		{"bom not on operation", func(c *TechCardInsert) { c.Operations[0].BomLineKeys = nil }, "operations[0].topstitch_lines[0].bom_line_key"},
		{"bom not on card", func(c *TechCardInsert) {
			c.Operations[0].TopstitchLines[0].BomLineKey = "MISSING"
			c.Operations[0].BomLineKeys = []string{"MISSING"}
		}, "operations[0].topstitch_lines[0].bom_line_key"},
		{"not thread", func(c *TechCardInsert) { c.BomItems[0].Section = BomSectionTrim }, "operations[0].topstitch_lines[0].bom_line_key"},
		{"not metres", func(c *TechCardInsert) { c.BomItems[0].Unit.String = "pcs" }, "operations[0].topstitch_lines[0].bom_line_key"},
		{"rows zero", func(c *TechCardInsert) { c.Operations[0].TopstitchLines[0].Rows = 0 }, "operations[0].topstitch_lines[0].rows"},
		{"rows five", func(c *TechCardInsert) { c.Operations[0].TopstitchLines[0].Rows = 5 }, "operations[0].topstitch_lines[0].rows"},
		{"no parts", func(c *TechCardInsert) { c.Operations[0].TopstitchLines[0].Parts = nil }, "operations[0].topstitch_lines[0].parts"},
		{"too many parts", func(c *TechCardInsert) {
			line := &c.Operations[0].TopstitchLines[0]
			line.Parts = nil
			for i := 0; i < 17; i++ {
				line.Parts = append(line.Parts, seamTestAnchor(topTestPiece, fmt.Sprintf("edge-%d", i)))
			}
		}, "operations[0].topstitch_lines[0].parts"},
		{"foreign piece", func(c *TechCardInsert) { c.Operations[0].TopstitchLines[0].Parts[0].PieceLineKey = "FOREIGN" }, "operations[0].topstitch_lines[0].parts[0].piece_line_key"},
		{"long anchored size", func(c *TechCardInsert) { c.Operations[0].TopstitchLines[0].AnchoredSize = strings.Repeat("x", 17) }, "operations[0].topstitch_lines[0].anchored_size"},
		{"size outside range", func(c *TechCardInsert) { c.Operations[0].TopstitchLines[0].Sizes[0].SizeId = 3 }, "operations[0].topstitch_lines[0].sizes[0].size_id"},
		{"size on empty range", func(c *TechCardInsert) { c.SizeIds = nil }, "operations[0].topstitch_lines[0].sizes[0].size_id"},
		{"size duplicate", func(c *TechCardInsert) { c.Operations[0].TopstitchLines[0].Sizes[0].SizeId = 1 }, "operations[0].topstitch_lines[0].sizes[1].size_id"},
		{"length zero", func(c *TechCardInsert) {
			c.Operations[0].TopstitchLines[0].Sizes[0].LineMm = decimal.NewNullDecimal(decimal.Zero)
		}, "operations[0].topstitch_lines[0].sizes[0].line_mm"},
		{"length scale", func(c *TechCardInsert) {
			c.Operations[0].TopstitchLines[0].Sizes[0].LineMm = decimal.NewNullDecimal(cutDec("2.001"))
		}, "operations[0].topstitch_lines[0].sizes[0].line_mm"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := topTestCard()
			tc.edit(in)
			err := ValidateTechCardTopstitchLines(in)
			var violation *ValidationError
			require.ErrorAs(t, err, &violation)
			require.Equal(t, tc.field, violation.Field)
		})
	}
}

func TestValidateTechCardTopstitchLinesCardCap(t *testing.T) {
	in := topTestCard()
	template := in.Operations[0].TopstitchLines[0]
	in.Operations[0].TopstitchLines = nil
	for i := 0; i <= TechCardTopstitchMaxPerCard; i++ {
		line := template
		line.LineKey = fmt.Sprintf("01J%023d", i)
		in.Operations[0].TopstitchLines = append(in.Operations[0].TopstitchLines, line)
	}
	err := ValidateTechCardTopstitchLines(in)
	var violation *ValidationError
	require.ErrorAs(t, err, &violation)
	require.Equal(t, "operations", violation.Field)
}

func TestDerivedLineNormsComposeTopstitchAndStrips(t *testing.T) {
	bom := []TechCardBomItem{{
		Id: 502, LineKey: cutTestLine, Section: BomSectionThread,
		Unit: sql.NullString{String: "m", Valid: true}, ThreadPerMm: decimal.NewNullDecimal(cutDec("2.5")),
	}}
	top := TechCardTopstitchLine{
		LineKey: topTestLine, BomLineKey: cutTestLine, Rows: 2,
		Sizes: []TechCardTopstitchLineSize{
			{SizeId: 1, LineMm: decimal.NewNullDecimal(cutDec("100"))},
			{SizeId: 2, LineMm: decimal.NewNullDecimal(cutDec("200"))},
		},
	}

	t.Run("rows and factor", func(t *testing.T) {
		n := DerivedLineNorms(bom, nil, []TechCardTopstitchLine{top}, []int{1, 2})[0]
		require.True(t, n.Complete)
		require.Equal(t, []string{"0.5", "1"}, cutNormStrings(n.BySize))
	})
	t.Run("mixed with strip", func(t *testing.T) {
		n := DerivedLineNorms(bom, []TechCardCutStrip{cutOneLength(500)}, []TechCardTopstitchLine{top}, []int{1, 2})[0]
		require.True(t, n.Complete)
		require.Equal(t, []string{"1", "1.5"}, cutNormStrings(n.BySize))
	})
	t.Run("missing size has no partial sum", func(t *testing.T) {
		short := top
		short.Sizes = short.Sizes[:1]
		n := DerivedLineNorms(bom, []TechCardCutStrip{cutOneLength(500)}, []TechCardTopstitchLine{short}, []int{1, 2})[0]
		require.False(t, n.Complete)
		require.Equal(t, []string{"1"}, cutNormStrings(n.BySize))
	})
	t.Run("null factor has no partial sum", func(t *testing.T) {
		unknown := append([]TechCardBomItem(nil), bom...)
		unknown[0].ThreadPerMm = decimal.NullDecimal{}
		n := DerivedLineNorms(unknown, []TechCardCutStrip{cutOneLength(500)}, []TechCardTopstitchLine{top}, []int{1, 2})[0]
		require.False(t, n.Complete)
		require.Empty(t, n.BySize)
	})
	t.Run("stale line has no number", func(t *testing.T) {
		stale := top
		stale.Stale = true
		n := DerivedLineNorms(bom, nil, []TechCardTopstitchLine{stale}, []int{1, 2})[0]
		require.False(t, n.Complete)
		require.Empty(t, n.BySize)
	})
}

func TestDerivedLineManualNormReturnsWhenLastLineGoes(t *testing.T) {
	bom := []TechCardBomItem{{
		Id: 502, LineKey: cutTestLine, Section: BomSectionThread,
		Unit: sql.NullString{String: "m", Valid: true}, ThreadPerMm: decimal.NewNullDecimal(cutDec("2.5")),
	}}
	manual := TechCardColorwayUsage{
		BomItemId:         sql.NullInt64{Int64: 502, Valid: true},
		Consumption:       decimal.NewNullDecimal(cutDec("9")),
		ConsumptionSource: sql.NullString{String: ConsumptionSourceManual, Valid: true},
	}
	top := TechCardTopstitchLine{
		LineKey: topTestLine2, BomLineKey: cutTestLine, Rows: 1,
		Sizes: []TechCardTopstitchLineSize{{SizeId: 1, LineMm: decimal.NewNullDecimal(cutDec("200"))}},
	}
	norms := DerivedLineNorms(bom, nil, []TechCardTopstitchLine{top}, []int{1})
	resolved := ResolveDerivedLineUsages([]TechCardColorwayUsage{manual}, bom, norms)
	require.Equal(t, "0.5", resolved[0].SizeConsumptions[0].Consumption.String())

	stored := resolved[0].StoredRow()
	require.Equal(t, "9", stored.Consumption.Decimal.String())
	require.Equal(t, ConsumptionSourceManual, stored.ConsumptionSource.String)
	restored := ResolveDerivedLineUsages([]TechCardColorwayUsage{stored}, bom, nil)
	require.Equal(t, "9", restored[0].Consumption.Decimal.String())
}

func TestCarryStoredTopstitchLinesForUnawareWriter(t *testing.T) {
	stored := topTestCard()
	stored.Operations[0].TopstitchLines[0].SourceFingerprint = sql.NullString{String: strings.Repeat("a", 64), Valid: true}
	in := topTestCard()
	in.Operations[0].TopstitchLines = nil
	in.SizeIds = []int{1}
	CarryStoredTopstitchLines(in, stored)
	require.Len(t, in.Operations[0].TopstitchLines, 1)
	require.Len(t, in.Operations[0].TopstitchLines[0].Sizes, 1)
	require.Equal(t, stored.Operations[0].TopstitchLines[0].SourceFingerprint, in.Operations[0].TopstitchLines[0].SourceFingerprint)

	emptyRange := topTestCard()
	emptyRange.Operations[0].TopstitchLines = nil
	emptyRange.SizeIds = nil
	CarryStoredTopstitchLines(emptyRange, stored)
	require.Len(t, emptyRange.Operations[0].TopstitchLines, 1)
	require.Empty(t, emptyRange.Operations[0].TopstitchLines[0].Sizes,
		"an unaware restore cannot carry measurements into an empty card range")

	aware := topTestCard()
	aware.TopstitchLinesAware = true
	aware.Operations[0].TopstitchLines = nil
	CarryStoredTopstitchLines(aware, stored)
	require.Empty(t, aware.Operations[0].TopstitchLines)
}

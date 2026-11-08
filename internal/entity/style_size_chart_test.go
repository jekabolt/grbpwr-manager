package entity

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestStyleSizeChartPointFingerprint(t *testing.T) {
	shrink := decimal.NullDecimal{Decimal: decimal.RequireFromString("2.5"), Valid: true}
	a := StyleSizeChartPointFingerprint([]string{"piece-b", "PIECE-A", "piece-a"}, "piece-source", shrink)
	b := StyleSizeChartPointFingerprint([]string{"PIECE-A", "PIECE-B"}, "piece-source", shrink)
	require.Len(t, a, 64)
	require.Equal(t, a, b, "the current source fingerprint already projects the sorted basis")

	require.NotEqual(t, a, StyleSizeChartPointFingerprint([]string{"PIECE-A", "PIECE-B"}, "changed-source", shrink))
	require.NotEqual(t, a, StyleSizeChartPointFingerprint([]string{"PIECE-A", "PIECE-B"}, "piece-source",
		decimal.NullDecimal{Decimal: decimal.NewFromInt(3), Valid: true}))
	require.Empty(t, StyleSizeChartPointFingerprint(nil, "piece-source", shrink))
}

func TestValidateStyleSizeChartPoints(t *testing.T) {
	valid := func() []StyleSizeChartPoint {
		return []StyleSizeChartPoint{{MeasurementNameID: 1, PomCode: "length-cb", Position: 1,
			TolMinusCm: decimal.Zero, TolPlusCm: decimal.NewFromInt(1)}}
	}
	cases := []struct {
		name   string
		mutate func([]StyleSizeChartPoint) []StyleSizeChartPoint
		field  string
	}{
		{"position zero", func(p []StyleSizeChartPoint) []StyleSizeChartPoint { p[0].Position = 0; return p }, "points[0].position"},
		{"negative tolerance", func(p []StyleSizeChartPoint) []StyleSizeChartPoint {
			p[0].TolMinusCm = decimal.NewFromInt(-1)
			return p
		}, "points[0].tol_minus_cm"},
		{"tolerance scale", func(p []StyleSizeChartPoint) []StyleSizeChartPoint {
			p[0].TolPlusCm = decimal.RequireFromString("0.125")
			return p
		}, "points[0].tol_plus_cm"},
		{"bad code", func(p []StyleSizeChartPoint) []StyleSizeChartPoint { p[0].PomCode = "CB"; return p }, "points[0].pom_code"},
		{"duplicate position", func(p []StyleSizeChartPoint) []StyleSizeChartPoint {
			return append(p, StyleSizeChartPoint{MeasurementNameID: 2, Position: 1})
		}, "points[1].position"},
		{"duplicate code", func(p []StyleSizeChartPoint) []StyleSizeChartPoint {
			return append(p, StyleSizeChartPoint{MeasurementNameID: 2, Position: 2, PomCode: "length-cb"})
		}, "points[1].pom_code"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ve *ValidationError
			require.ErrorAs(t, ValidateStyleSizeChartPoints(tc.mutate(valid())), &ve)
			require.Equal(t, tc.field, ve.Field)
		})
	}
}

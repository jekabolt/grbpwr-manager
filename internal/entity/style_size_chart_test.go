package entity

import (
	"math"
	"strings"
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

func TestValidateStyleSizeChartPicture(t *testing.T) {
	require.NoError(t, ValidateStyleSizeChartPicture(nil), "nil keeps the stored picture")
	require.NoError(t, ValidateStyleSizeChartPicture(&StyleSizeChartPicture{}), "empty source clears it")

	icon := &StyleSizeChartPicture{
		Source: " icon ", IconKey: " joggers ", MediaID: 5, MediaURL: "https://x/stale.jpg",
		Lines: []StyleSizeChartLine{
			{X1: 0.123456, Y1: 1, X2: 0, Y2: 0.5, Position: 7},
			{MeasurementNameID: 3, X1: 0.5, Y1: 0.5, X2: 0.5, Y2: 0.5, Position: 2},
			{X1: 0.1, Y1: 0.1, X2: 0.2, Y2: 0.2, Position: 7},
		},
	}
	require.NoError(t, ValidateStyleSizeChartPicture(icon))
	require.Equal(t, StyleSizeChartPicture{
		Source: StyleSizeChartPictureSourceIcon, IconKey: "joggers",
		Lines: []StyleSizeChartLine{
			{MeasurementNameID: 3, X1: 0.5, Y1: 0.5, X2: 0.5, Y2: 0.5, Position: 1},
			{X1: 0.12346, Y1: 1, X2: 0, Y2: 0.5, Position: 2},
			{X1: 0.1, Y1: 0.1, X2: 0.2, Y2: 0.2, Position: 3},
		},
	}, *icon, "trimmed, other-source fields and the url dropped, rounded, stable order renumbered 1..n")

	sketch := &StyleSizeChartPicture{Source: StyleSizeChartPictureSourceSketch, IconKey: "joggers", MediaID: 9}
	require.NoError(t, ValidateStyleSizeChartPicture(sketch))
	require.Empty(t, sketch.IconKey)
	require.Equal(t, 9, sketch.MediaID)

	tooMany := make([]StyleSizeChartLine, StyleSizeChartPictureMaxLines+1)
	for name, tc := range map[string]struct {
		in    StyleSizeChartPicture
		field string
	}{
		"lines without source": {StyleSizeChartPicture{Lines: []StyleSizeChartLine{{}}}, "picture.source"},
		"unknown source":       {StyleSizeChartPicture{Source: "photo"}, "picture.source"},
		"icon without key":     {StyleSizeChartPicture{Source: "icon"}, "picture.icon_key"},
		"icon key charset":     {StyleSizeChartPicture{Source: "icon", IconKey: "Joggers!"}, "picture.icon_key"},
		"icon key too long":    {StyleSizeChartPicture{Source: "icon", IconKey: strings.Repeat("a", 65)}, "picture.icon_key"},
		"upload without media": {StyleSizeChartPicture{Source: "upload"}, "picture.media_id"},
		"too many lines":       {StyleSizeChartPicture{Source: "upload", MediaID: 1, Lines: tooMany}, "picture.lines"},
		"negative name": {StyleSizeChartPicture{Source: "upload", MediaID: 1,
			Lines: []StyleSizeChartLine{{MeasurementNameID: -1}}}, "picture.lines[0].measurement_name_id"},
		"x above one": {StyleSizeChartPicture{Source: "upload", MediaID: 1,
			Lines: []StyleSizeChartLine{{}, {X2: 1.00001}}}, "picture.lines[1].x2"},
		"NaN": {StyleSizeChartPicture{Source: "upload", MediaID: 1,
			Lines: []StyleSizeChartLine{{Y1: math.NaN()}}}, "picture.lines[0].y1"},
	} {
		in := tc.in
		err := ValidateStyleSizeChartPicture(&in)
		var ve *ValidationError
		require.ErrorAs(t, err, &ve, name)
		require.Equal(t, tc.field, ve.Field, name)
	}
}

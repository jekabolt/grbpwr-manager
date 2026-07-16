package dto

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

func ns2(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

// TestConvertPbProductionRunMarkers covers the nesting-marker import validation + conversion
// (gap-07 v2 E): a full marker round-trips, an unset source defaults to manual, and each guard
// (source / dimensions / efficiency / lengths) rejects.
func TestConvertPbProductionRunMarkers(t *testing.T) {
	got, err := convertPbProductionRunMarkers([]*pb_common.ProductionRunMarker{
		{
			Source:     pb_common.ProductionMarkerSource_PRODUCTION_MARKER_SOURCE_GERBER,
			MarkerName: "M-42", SizeId: 3, MaterialId: 7,
			MarkerWidth: dec("150"), LayLength: dec("6.4"), UnitsPerMarker: 12,
			EfficiencyPct: dec("87.5"), MarkerFileUrl: "https://cdn/x.mrk", Notes: "two-way",
		},
		{Source: pb_common.ProductionMarkerSource_PRODUCTION_MARKER_SOURCE_UNKNOWN}, // defaults to manual
		nil, // skipped
	})
	require.NoError(t, err)
	require.Len(t, got, 2)

	m := got[0]
	require.Equal(t, entity.ProductionMarkerSourceGerber, m.Source)
	require.Equal(t, "M-42", m.MarkerName.String)
	require.Equal(t, int32(3), m.SizeId.Int32)
	require.Equal(t, int32(7), m.MaterialId.Int32)
	require.Equal(t, "150", m.MarkerWidth.Decimal.String())
	require.Equal(t, "6.4", m.LayLength.Decimal.String())
	require.Equal(t, int32(12), m.UnitsPerMarker.Int32)
	require.Equal(t, "87.5", m.EfficiencyPct.Decimal.String())
	require.Equal(t, "https://cdn/x.mrk", m.MarkerFileUrl.String)
	require.Equal(t, "two-way", m.Notes.String)

	require.Equal(t, entity.ProductionMarkerSourceManual, got[1].Source, "UNKNOWN source defaults to manual")

	// empty / nil input → no markers.
	none, err := convertPbProductionRunMarkers(nil)
	require.NoError(t, err)
	require.Nil(t, none)

	// failures.
	bad := map[string]*pb_common.ProductionRunMarker{
		"negative width":  {MarkerWidth: dec("-1")},
		"negative length": {LayLength: dec("-1")},
		"negative units":  {UnitsPerMarker: -1},
		"efficiency>100":  {EfficiencyPct: dec("101")},
		"efficiency<0":    {EfficiencyPct: dec("-0.1")},
		"bad decimal":     {MarkerWidth: dec("abc")},
		"name too long":   {MarkerName: strings.Repeat("x", maxVarchar191+1)},
		"url too long":    {MarkerFileUrl: strings.Repeat("x", maxVarchar512+1)},
	}
	for name, in := range bad {
		_, err := convertPbProductionRunMarkers([]*pb_common.ProductionRunMarker{in})
		require.Error(t, err, name)
	}
}

// TestProductionRunMarkersToPb round-trips a stored marker back to pb, with null-safe int/decimal
// fields collapsing to 0 / nil.
func TestProductionRunMarkersToPb(t *testing.T) {
	require.Nil(t, productionRunMarkersToPb(nil))

	out := productionRunMarkersToPb([]entity.ProductionRunMarker{
		{
			Source: entity.ProductionMarkerSourceOptitex, MarkerName: ns2("L1"),
			SizeId: ni32(2), MarkerWidth: nd2("140"), EfficiencyPct: nd2("90"),
		},
		{Source: entity.ProductionMarkerSourceManual}, // all-null
	})
	require.Len(t, out, 2)
	require.Equal(t, pb_common.ProductionMarkerSource_PRODUCTION_MARKER_SOURCE_OPTITEX, out[0].Source)
	require.Equal(t, "L1", out[0].MarkerName)
	require.Equal(t, int32(2), out[0].SizeId)
	require.Equal(t, "140", out[0].MarkerWidth.GetValue())
	require.Equal(t, "90", out[0].EfficiencyPct.GetValue())

	require.Equal(t, int32(0), out[1].SizeId, "null size collapses to 0")
	require.Nil(t, out[1].MarkerWidth, "null decimal collapses to nil")
	require.Empty(t, out[1].MarkerName)
}

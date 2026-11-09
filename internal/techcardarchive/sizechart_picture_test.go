package techcardarchive

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// The size chart picture (0459) is an optional object of sizechart.json: it round-trips field for
// field, and a chart written before it existed reads as a chart with no picture.
func TestSizeChartPictureRoundTripAndOldArchive(t *testing.T) {
	in := SizeChart{
		Cells: []SizeChartCell{{SizeName: "m", Measurement: "chest", Value: "52"}},
		Picture: &SizeChartPicture{
			Source: "upload", MediaRef: 4050,
			Lines: []SizeChartLine{
				{Measurement: "chest", X1: 0.1, Y1: 0.42, X2: 0.9, Y2: 0.42, Position: 1},
				{Measurement: "", X1: 0.5, Y1: 0, X2: 0.5, Y2: 1, Position: 2},
			},
		},
	}
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"media_ref":4050`)
	require.Contains(t, string(raw), `"measurement":""`, "an unpicked line says so explicitly")

	var out SizeChart
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Equal(t, in, out)

	icon, err := json.Marshal(SizeChart{Cells: []SizeChartCell{}, Picture: &SizeChartPicture{Source: "icon", IconKey: "tee"}})
	require.NoError(t, err)
	require.JSONEq(t, `{"cells":[],"picture":{"source":"icon","icon_key":"tee"}}`, string(icon))

	var old SizeChart
	require.NoError(t, json.Unmarshal([]byte(`{"cells":[],"shrinkage_pct":"2"}`), &old))
	require.Nil(t, old.Picture)
	noPicture, err := json.Marshal(old)
	require.NoError(t, err)
	require.NotContains(t, string(noPicture), "picture", "a chart without a picture writes no key")
}

package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

func TestTechCardColorwayRequiresCanonicalDictionaryColorCode(t *testing.T) {
	tests := []struct {
		name      string
		colorways []*pb_common.TechCardColorway
		wantErr   string
	}{
		{
			name:      "missing",
			colorways: []*pb_common.TechCardColorway{{Name: "black", Code: "BLK", Hex: "#000000"}},
			wantErr:   "color_code must be exactly 3 uppercase characters",
		},
		{
			name:      "lowercase is not canonical",
			colorways: []*pb_common.TechCardColorway{{Name: "black", ColorCode: "blk"}},
			wantErr:   "color_code must be exactly 3 uppercase characters",
		},
		{
			name:      "unknown dictionary code",
			colorways: []*pb_common.TechCardColorway{{Name: "custom", ColorCode: "ZZZ"}},
			wantErr:   "is not in the color dictionary",
		},
		{
			name: "duplicate dictionary code",
			colorways: []*pb_common.TechCardColorway{
				{Name: "black one", ColorCode: "BLK"},
				{Name: "black two", ColorCode: "BLK"},
			},
			wantErr: "duplicate color_code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseTechCardColorways(tt.colorways, nil, 0, nil, 0)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestTechCardColorwayColorCodeRoundTripResolvesDictionaryColor(t *testing.T) {
	parsed, err := parseTechCardColorways([]*pb_common.TechCardColorway{{
		Name:      "Runway black",
		Code:      "ARTICLE-01",
		ColorCode: "BLK",
		Pantone:   "19-0303 TCX",
		Hex:       "#010101",
	}}, nil, 0, nil, 0)
	require.NoError(t, err)
	require.Equal(t, "BLK", parsed[0].ColorCode)

	got := techCardColorwaysToPb([]entity.TechCardColorway{parsed[0]}, nil, nil)
	require.Len(t, got, 1)
	require.Equal(t, "BLK", got[0].ColorCode)
	require.NotNil(t, got[0].DictionaryColor)
	require.Equal(t, int32(1), got[0].DictionaryColor.Id)
	require.Equal(t, "black", got[0].DictionaryColor.Name)
	require.Equal(t, "#000000", got[0].DictionaryColor.Hex)
	// Free-text PLM metadata remains display information and never overrides dictionary identity.
	require.Equal(t, "ARTICLE-01", got[0].Code)
	require.Equal(t, "19-0303 TCX", got[0].Pantone)
	require.Equal(t, "#010101", got[0].Hex)
}

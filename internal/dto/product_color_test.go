package dto

import (
	"testing"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

func TestColorwayBodyAcceptsOnlyDictionaryColorIdentity(t *testing.T) {
	tests := []struct {
		name     string
		code     string
		override *string
		wantErr  string
	}{
		{name: "missing", wantErr: "color_code must be exactly 3 uppercase characters"},
		{name: "lowercase", code: "blk", wantErr: "color_code must be exactly 3 uppercase characters"},
		{name: "unknown", code: "ZZZ", wantErr: "is not in the color dictionary"},
		{name: "invalid override", code: "BLK", override: stringPointer("black"), wantErr: "color_hex_override must be #RRGGBB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := convertMerchInsertToEntity(&pb_common.ColorwayMerchandisingInsert{
				ColorCode:        tt.code,
				ColorHexOverride: tt.override,
			}, "")
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestColorwayBodyResolvesDictionaryDisplayAndKeepsOptionalOverride(t *testing.T) {
	got, err := convertMerchInsertToEntity(&pb_common.ColorwayMerchandisingInsert{
		ColorCode:        "BLK",
		ColorHexOverride: stringPointer("#010101"),
	}, "")
	require.NoError(t, err)
	require.Equal(t, "BLK", got.ColorCode)
	require.Equal(t, "black", got.Color)
	require.True(t, got.ColorHexOverride.Valid)
	require.Equal(t, "#010101", got.ColorHexOverride.String)

	resolved := dictionaryColorToPb(got.ColorCode)
	require.Equal(t, "BLK", resolved.Code)
	require.Equal(t, "black", resolved.Name)
	require.Equal(t, "#000000", resolved.Hex)
}

func TestFilterConditionsAcceptOnlyDictionaryColorCodes(t *testing.T) {
	tests := []struct {
		name    string
		codes   []string
		wantErr string
	}{
		{name: "lowercase", codes: []string{"blk"}, wantErr: "must be exactly 3 uppercase characters"},
		{name: "unknown", codes: []string{"ZZZ"}, wantErr: "is not in the color dictionary"},
		{name: "valid", codes: []string{"BLK", "WHT"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConvertPBCommonFilterConditionsToEntity(&pb_common.FilterConditions{ColorCodes: tt.codes})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.codes, got.ColorCodes)
		})
	}
}

func stringPointer(value string) *string { return &value }

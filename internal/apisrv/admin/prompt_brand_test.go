package admin

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTranslateFieldsPromptSpeaksForTheTeamsBrand(t *testing.T) {
	shop := &translateFieldsIn{langName: "English", codes: []string{"fr"}, names: []string{"name"}}
	pack := &translateFieldsIn{langName: "English", codes: []string{"fr"}, names: []string{"name"}, techpack: true}

	// Team #1: today's wording byte for byte (TestTranslateFieldsTechpackContext pins the rest).
	require.True(t, strings.HasPrefix(translateFieldsSystemPrompt(shop, promptBrand{House: true}),
		"You are a professional fashion e-commerce translator for the brand grbpwr.\n"))
	require.True(t, strings.HasPrefix(translateFieldsSystemPrompt(pack, promptBrand{House: true}),
		"You are a garment technologist translating a tech pack for a sewing factory (brand grbpwr).\n"))

	named := promptBrand{Name: "Acme Studio"}
	require.True(t, strings.HasPrefix(translateFieldsSystemPrompt(shop, named),
		"You are a professional fashion e-commerce translator for the brand \"Acme Studio\".\n"))
	require.True(t, strings.HasPrefix(translateFieldsSystemPrompt(pack, named),
		"You are a garment technologist translating a tech pack for a sewing factory (brand \"Acme Studio\").\n"))

	neutral := promptBrand{}
	require.True(t, strings.HasPrefix(translateFieldsSystemPrompt(shop, neutral),
		"You are a professional fashion e-commerce translator for a fashion brand.\n"))
	require.True(t, strings.HasPrefix(translateFieldsSystemPrompt(pack, neutral),
		"You are a garment technologist translating a tech pack for a sewing factory.\n"))

	for _, b := range []promptBrand{named, neutral} {
		require.NotContains(t, strings.ToLower(translateFieldsSystemPrompt(shop, b)), "grbpwr")
		require.NotContains(t, strings.ToLower(translateFieldsSystemPrompt(pack, b)), "grbpwr")
	}
}

func TestTranslateBrandIsHouseWithTeamsOff(t *testing.T) {
	s := &Server{}
	require.Equal(t, promptBrand{House: true}, s.translateBrand(t.Context()))
}

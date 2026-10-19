package dto

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The admin dictionary carries the hidden model-photo card id; the storefront dictionary (which
// never sets it) carries 0.
func TestDictionaryCarriesTheModelPhotoTechCardIDOnlyWhenSet(t *testing.T) {
	require.EqualValues(t, 72, ConvertToCommonDictionary(Dict{ModelPhotoTechCardID: 72}).GetModelPhotoTechCardId())
	require.Zero(t, ConvertToCommonDictionary(Dict{}).GetModelPhotoTechCardId())
}

package cache

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// The hidden model-photo card id: a full dictionary load sets it, a partial DictionaryInfo (0) keeps
// the known id, and 0 never matches a card.
func TestModelPhotoTechCardIDSurvivesAPartialRefresh(t *testing.T) {
	prev := GetModelPhotoTechCardID()
	t.Cleanup(func() { SetModelPhotoTechCardID(prev) })

	SetModelPhotoTechCardID(0)
	require.False(t, IsModelPhotoTechCard(0), "an unknown id must not match the zero id")

	RefreshDictionary(&entity.DictionaryInfo{ModelPhotoTechCardID: 72})
	require.Equal(t, 72, GetModelPhotoTechCardID())
	require.True(t, IsModelPhotoTechCard(72))
	require.False(t, IsModelPhotoTechCard(71))

	RefreshDictionary(&entity.DictionaryInfo{Tags: []entity.TagDict{{ID: 1, Code: "x", Name: "X"}}})
	require.Equal(t, 72, GetModelPhotoTechCardID(), "a partial refresh must keep the system card id")
}

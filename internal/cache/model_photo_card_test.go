package cache

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// The hidden model-photo card id: a full dictionary load sets it, a partial DictionaryInfo (0) keeps
// the known id, and 0 never matches a card.
func TestModelPhotoTechCardIDSurvivesAPartialRefresh(t *testing.T) {
	resetForTest(t)
	ctx := context.Background()
	SetModelPhotoTechCardID(ctx, 0)
	require.False(t, IsModelPhotoTechCard(ctx, 0), "an unknown id must not match the zero id")

	RefreshDictionary(ctx, &entity.DictionaryInfo{ModelPhotoTechCardID: 72})
	require.Equal(t, 72, GetModelPhotoTechCardID(ctx))
	require.True(t, IsModelPhotoTechCard(ctx, 72))
	require.False(t, IsModelPhotoTechCard(ctx, 71))

	RefreshDictionary(ctx, &entity.DictionaryInfo{Tags: []entity.TagDict{{ID: 1, Code: "x", Name: "X"}}})
	require.Equal(t, 72, GetModelPhotoTechCardID(ctx), "a partial refresh must keep the system card id")
}

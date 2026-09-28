package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// The colourway's optimistic-lock token (the style's shared tech_card.lock_version) must ride out
// on both Colorway projections: the admin echoes it into expected_colorway_version, and a zero here
// made every save of an edited style's colourway answer ABORTED ("modified concurrently").
func TestColorwayProjectionsCarryLockVersion(t *testing.T) {
	c := &entity.Colorway{Id: 39, StyleId: 49, LockVersion: 2}

	full, err := ConvertToPbProductFull(&entity.ColorwayFull{Product: c})
	require.NoError(t, err)
	require.Equal(t, int32(2), full.GetColorway().GetLockVersion())

	common, err := ConvertEntityProductToCommon(c)
	require.NoError(t, err)
	require.Equal(t, int32(2), common.GetLockVersion())
}

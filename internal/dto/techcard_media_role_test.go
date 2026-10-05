package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// 0395: the moodboard picture role is parsed, validated and read back on the card.
func TestTechCardMediaRoleParseAndRoundTrip(t *testing.T) {
	items, err := parseTechCardMediaItems([]*pb_common.TechCardMediaItem{
		{MediaId: 1, Role: "target"}, {MediaId: 2}, {MediaId: 3, Role: "mood"},
	}, entity.TechCardMediaCategoryMoodboard)
	require.NoError(t, err)
	require.Equal(t, entity.TechCardMediaRoleTarget, items[0].Role)
	require.Equal(t, entity.TechCardMediaRoleNone, items[1].Role)

	_, err = parseTechCardMediaItems([]*pb_common.TechCardMediaItem{{MediaId: 1, Role: "hero"}}, entity.TechCardMediaCategoryMoodboard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "moodboard_media[0].role")

	for _, r := range []string{"", "target", "detail", "material", "mood"} {
		require.True(t, entity.IsTechCardMediaRole(entity.TechCardMediaRole(r)), r)
	}
}

package admin

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// 64-DEFERRED E3: every prompt that numbers board pictures names the designer's role next to the
// number — counted by the ATTACHED list, so a missing picture never shifts a role onto a neighbour.
func designRoleCard() *entity.TechCard {
	card := &entity.TechCard{}
	card.Media = []entity.TechCardMediaItem{
		{MediaId: 11, Category: entity.TechCardMediaCategoryMoodboard, Role: entity.TechCardMediaRoleMood},
		{MediaId: 12, Category: entity.TechCardMediaCategoryMoodboard}, // unassigned
		{MediaId: 13, Category: entity.TechCardMediaCategoryMoodboard, Role: entity.TechCardMediaRoleTarget},
		{MediaId: 14, Category: entity.TechCardMediaCategoryMoodboard, Role: entity.TechCardMediaRoleMaterial},
		{MediaId: 15, Category: entity.TechCardMediaCategoryTechnical, Role: entity.TechCardMediaRoleDetail},
	}
	return card
}

func TestDesignPictureRolesInEveryBoardPrompt(t *testing.T) {
	card := designRoleCard()
	// 11 did not go: 12 is picture 1, 13 picture 2, 14 picture 3.
	attached := []int{12, 13, 14}
	mood := &pb_common.DesignMoodSnapshot{Callouts: []*pb_common.DesignMoodCallout{{MediaId: 13, Text: "ROLEPIN"}}}

	for name, p := range map[string]string{
		"draft":        designDraftIdeaPrompt(card, mood, attached),
		"construction": designConstructionUserPrompt(card, mood, attached, nil),
		"quiz":         designQuizUserPrompt(card, mood, attached, "jacket", ""),
	} {
		require.Contains(t, p, "- picture 2 (target garment)\n", name)
		require.Contains(t, p, "- picture 3 (material reference)\n", name)
		require.NotContains(t, p, "picture 1 (", name) // unassigned picture is named bare
		require.NotContains(t, p, "mood only", name)   // the mood picture did not go
		require.NotContains(t, p, "detail reference", name)
		require.Contains(t, p, "- picture 2 (target garment): ROLEPIN", name)
	}
	q := designQuizUserPrompt(card, mood, attached, "jacket", "")
	require.Contains(t, q, "settled only by a target or detail picture")
}

func TestDesignPictureRolesAbsentLeaveBoardUnchanged(t *testing.T) {
	card := &entity.TechCard{}
	card.Media = []entity.TechCardMediaItem{{MediaId: 1, Category: entity.TechCardMediaCategoryMoodboard}}
	mood := &pb_common.DesignMoodSnapshot{Note: "n"}
	require.Nil(t, designBoardRoles(card))
	require.Equal(t, designBoardPromptBody(mood, []int{1}), designBoardPromptBodyRoles(mood, []int{1}, designBoardRoles(card)))
	q := designQuizUserPrompt(card, mood, []int{1}, "jacket", "")
	require.False(t, strings.Contains(q, "Picture roles decide"))
}

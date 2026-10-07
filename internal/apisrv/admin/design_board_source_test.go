package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// designMoodCardWithRefOnBoard — designMoodCard with the person's reference photo (designRefMediaID,
// labelled front in designBandWith) lying on the moodboard as the garment: since Ф4 a flat run reads a
// person's label only there (designRunRefsFor).
func designMoodCardWithRefOnBoard() *entity.TechCard {
	card := designMoodCard()
	card.Media = append(card.Media, entity.TechCardMediaItem{MediaId: designRefMediaID,
		Category: entity.TechCardMediaCategoryMoodboard, Kind: entity.TechCardMediaMoodboard,
		Role: entity.TechCardMediaRoleTarget})
	return card
}

func withBoardSource(t *testing.T) {
	t.Helper()
	old := designBoardIsTheSource
	designBoardIsTheSource = true
	t.Cleanup(func() { designBoardIsTheSource = old })
}

func TestTheBoardIsTheSourceInProduction(t *testing.T) {
	require.True(t, designBoardIsTheSourceDefault, "101 Ф4: the moodboard is the only source of a run's pictures")
}

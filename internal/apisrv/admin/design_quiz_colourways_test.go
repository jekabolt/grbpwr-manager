package admin

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Owner, 06.10 (card 51): "how many colourways" went stale on a BOM edit while the card still had one
// colourway. Colourway answers read the colourways, not the BOM.
func TestDesignQuizColourwayAnswerIgnoresBOM(t *testing.T) {
	card := &entity.TechCard{}
	card.BomItems = []entity.TechCardBomItem{{Name: "shell", Composition: sql.NullString{String: "cotton", Valid: true}}}
	card.Colorways = []entity.TechCardColorway{{Name: "black", ColorCode: "black"}}
	q := entity.DesignQuizQuestion{ID: "colourway_count", DecisionKey: "colourway_count", Category: "design", Part: "whole"}
	answers := []entity.TechCardQuizAnswer{{Question: q, Selected: []string{"one"}}}
	st := designQuizStateOf(card, entity.StyleSizeChart{}, nil, nil)
	st.stamp(answers)
	require.Equal(t, entity.DesignQuizTopicColourways, answers[0].Topic)

	card.BomItems[0].Composition = sql.NullString{String: "wool", Valid: true}
	designQuizStateOf(card, entity.StyleSizeChart{}, nil, nil).mark(answers)
	require.False(t, answers[0].Stale, "a BOM edit must not stale a colourway answer")

	card.Colorways = append(card.Colorways, entity.TechCardColorway{Name: "ecru", ColorCode: "ecru"})
	designQuizStateOf(card, entity.StyleSizeChart{}, nil, nil).mark(answers)
	require.True(t, answers[0].Stale)
	require.Contains(t, answers[0].StaleChanges[0], "colourways: 1 → 2")

	// A row stamped under the old topic (materials, before 06.10) is fresh, not stale on the BOM.
	old := []entity.TechCardQuizAnswer{{Question: q, Selected: []string{"one"}, Topic: entity.DesignQuizTopicMaterials,
		Facts: []entity.DesignQuizFact{{Label: "shell", Value: "cotton"}}}}
	designQuizStateOf(card, entity.StyleSizeChart{}, nil, nil).mark(old)
	require.False(t, old[0].Stale)
}

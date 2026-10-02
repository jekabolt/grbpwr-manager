package designgen

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// T07 (owner item 7): a detail-only flat run draws ONLY the detail. WORDS ride along as garment
// context, never under the bare «garment:» label, and the craft says «only this detail, no views».
// MUTATION: revert the label switch in composePrompt or the flatOnlyDetail paragraph in flatCraft —
// the matching assertion goes red.
func TestDetailOnlyFlatPromptDrawsOnlyTheDetail(t *testing.T) {
	run := entity.DesignRun{Kind: entity.DesignRunKindFlat}
	in := runInputs{GarmentNote: "boxy work jacket, two chest pockets"}

	got := composePrompt(run, runParams{Views: []string{entity.DesignViewDetail}, Layout: layoutOne}, in, nil)
	require.NotContains(t, got, "garment:\n")
	require.Contains(t, got, flatDetailGarmentLabel+":\nboxy work jacket")
	require.Contains(t, got, flatOnlyDetail)

	two := composePrompt(run, runParams{Views: []string{entity.DesignViewDetail, entity.DesignViewDetail}, Layout: layoutOne}, in, nil)
	require.Contains(t, two, flatDetailGarmentLabel+":\n")
	require.Contains(t, two, flatOnlyDetails)
	require.NotContains(t, two, flatOnlyDetail)

	// Positive control: a garment run keeps its «garment:» label and has no detail-only sentence.
	sides := composePrompt(run, runParams{Views: []string{entity.DesignViewFront, entity.DesignViewBack}, Layout: layoutOne}, in, nil)
	require.Contains(t, sides, "garment:\nboxy work jacket")
	require.NotContains(t, sides, "Draw ONLY")
}

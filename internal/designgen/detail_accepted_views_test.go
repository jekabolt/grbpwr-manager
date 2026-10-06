package designgen

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// T8 (owner 06.10): a detail run carries the card's FRONT / BACK flats as the garment's accepted
// views — captioned as such, numbered in the craft, and ruling over the photos.
// MUTATION: drop the acceptedViews branch in referenceList — the captions fall back to «current
// state of the garment» and the paragraph disappears.
func TestDetailRunCaptionsTheAcceptedViews(t *testing.T) {
	run := entity.DesignRun{Kind: entity.DesignRunKindFlat}
	p := runParams{Views: []string{entity.DesignViewDetail}, Layout: layoutOne, DetailSlotIDs: []int{5}}
	in := runInputs{
		Refs: []inputRef{{MediaID: 1, Role: entity.DesignViewFront}},
		Slots: []inputSlot{
			{ViewKey: entity.DesignViewBack, MediaID: 12},
			{ViewKey: entity.DesignViewFront, MediaID: 11},
			{ViewKey: entity.DesignViewDetail, SlotID: 5, DetailName: "strap"},
		},
	}
	list := referenceList(run.Kind, p, in)
	require.Len(t, list, 3)
	require.Equal(t, 1, list[0].MediaID, "the photo first on the flat route")
	require.Equal(t, 11, list[1].MediaID)
	require.True(t, list[1].IsAcceptedView)
	require.Contains(t, list[1].Caption, "accepted technical flat")
	require.NotContains(t, list[1].Caption, "current state")
	require.Equal(t, 12, list[2].MediaID)
	require.True(t, list[2].IsAcceptedView)

	got := composePrompt(run, p, in, list)
	require.Contains(t, got, "Images 2 and 3 are this garment's accepted technical flats")
	require.Contains(t, got, "Where a photo and the flats differ, follow the flats.")
	require.Contains(t, got, flatOnlyDetail)
}

// A garment run never gets the accepted-views words, even when a plate rides (use_flat_slots).
func TestViewsRunKeepsTheCurrentStateCaption(t *testing.T) {
	run := entity.DesignRun{Kind: entity.DesignRunKindFlat}
	p := runParams{Views: []string{entity.DesignViewFront, entity.DesignViewBack}, Layout: layoutOne}
	in := runInputs{Slots: []inputSlot{{ViewKey: entity.DesignViewFront, MediaID: 11}}}
	list := referenceList(run.Kind, p, in)
	require.Len(t, list, 1)
	require.False(t, list[0].IsAcceptedView)
	require.Contains(t, list[0].Caption, "current state of the garment")
	require.NotContains(t, composePrompt(run, p, in, list), "accepted technical flat")
}

// Codex gate: a detail run that asked for its plates itself (use_flat_slots) gets no «accepted»
// words, and a side plate is never an accepted view.
func TestDetailRunWithOwnPlatesIsNotTheAcceptedViews(t *testing.T) {
	run := entity.DesignRun{Kind: entity.DesignRunKindFlat}
	in := runInputs{Slots: []inputSlot{
		{ViewKey: entity.DesignViewFront, MediaID: 11},
		{ViewKey: entity.DesignViewSideL, MediaID: 13},
	}}
	p := runParams{Views: []string{entity.DesignViewDetail}, Layout: layoutOne, UseFlatSlots: true}
	for _, rc := range referenceList(run.Kind, p, in) {
		require.False(t, rc.IsAcceptedView)
	}
	p.UseFlatSlots = false
	list := referenceList(run.Kind, p, in)
	require.True(t, list[0].IsAcceptedView)
	require.False(t, list[1].IsAcceptedView, "a side plate is never an accepted view")
}

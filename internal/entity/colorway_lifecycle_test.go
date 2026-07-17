package entity

import "testing"

// TestColorwayStatusWireNumbers locks the numeric values (contract decision R6). These MUST match the
// ColorwayLifecycleStatus proto enum and the product.lifecycle_status DB CHECK; changing one without
// the others silently corrupts every stored status.
func TestColorwayStatusWireNumbers(t *testing.T) {
	cases := map[ColorwayStatus]uint8{
		ColorwayStatusUnknown:  0,
		ColorwayStatusDraft:    1,
		ColorwayStatusActive:   2,
		ColorwayStatusHidden:   3,
		ColorwayStatusArchived: 4,
	}
	for s, want := range cases {
		if uint8(s) != want {
			t.Errorf("%s: wire number = %d, want %d", s, uint8(s), want)
		}
	}
}

func TestColorwayStatusIsValid(t *testing.T) {
	valid := []ColorwayStatus{ColorwayStatusDraft, ColorwayStatusActive, ColorwayStatusHidden, ColorwayStatusArchived}
	for _, s := range valid {
		if !s.IsValid() {
			t.Errorf("%s should be a valid writable status", s)
		}
	}
	invalid := []ColorwayStatus{ColorwayStatusUnknown, ColorwayStatus(5), ColorwayStatus(200)}
	for _, s := range invalid {
		if s.IsValid() {
			t.Errorf("status %d must be invalid (fail-closed)", uint8(s))
		}
	}
}

func TestColorwayStatusString(t *testing.T) {
	cases := map[ColorwayStatus]string{
		ColorwayStatusUnknown:  "unknown",
		ColorwayStatusDraft:    "draft",
		ColorwayStatusActive:   "active",
		ColorwayStatusHidden:   "hidden",
		ColorwayStatusArchived: "archived",
		ColorwayStatus(9):      "unknown",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("ColorwayStatus(%d).String() = %q, want %q", uint8(s), got, want)
		}
	}
}

// TestNextColorwayStatus is the full R6 lifecycle state machine: exactly the allowed edges, everything
// else rejected — including every command out of the terminal ARCHIVED state and every command out of
// an unknown (fail-closed) state.
func TestNextColorwayStatus(t *testing.T) {
	allTransitions := []ColorwayTransition{
		ColorwayTransitionPublish, ColorwayTransitionHide, ColorwayTransitionUnhide, ColorwayTransitionArchive, ColorwayTransitionRestore,
	}
	// allowed[from][transition] = expected target
	allowed := map[ColorwayStatus]map[ColorwayTransition]ColorwayStatus{
		ColorwayStatusDraft:  {ColorwayTransitionPublish: ColorwayStatusActive},
		ColorwayStatusActive: {ColorwayTransitionHide: ColorwayStatusHidden, ColorwayTransitionArchive: ColorwayStatusArchived},
		ColorwayStatusHidden: {ColorwayTransitionUnhide: ColorwayStatusActive, ColorwayTransitionArchive: ColorwayStatusArchived},
		// Archived: soft-terminal — its only edge is restore -> HIDDEN (admin unarchive-to-hidden). It
		// must NOT be able to go straight to ACTIVE (no restore->ACTIVE), which this table also asserts.
		ColorwayStatusArchived: {ColorwayTransitionRestore: ColorwayStatusHidden},
	}
	states := []ColorwayStatus{
		ColorwayStatusUnknown, ColorwayStatusDraft, ColorwayStatusActive, ColorwayStatusHidden, ColorwayStatusArchived,
	}
	for _, from := range states {
		for _, tr := range allTransitions {
			to, err := NextColorwayStatus(from, tr)
			want, ok := allowed[from][tr]
			if ok {
				if err != nil {
					t.Errorf("%s --%s--> expected %s, got error %v", from, tr, want, err)
				} else if to != want {
					t.Errorf("%s --%s--> = %s, want %s", from, tr, to, want)
				}
				continue
			}
			if err == nil {
				t.Errorf("%s --%s--> should be rejected, got %s", from, tr, to)
			}
		}
	}
}

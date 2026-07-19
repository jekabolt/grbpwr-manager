package entity

import (
	"errors"
	"fmt"
)

// ErrColorwayNotDraft is returned when an operation that is only valid for a DRAFT colourway (e.g.
// RelinkDraftColorway) targets a colourway in another state. The API layer maps it to
// FailedPrecondition.
var ErrColorwayNotDraft = errors.New("colourway is not a draft")

// ErrStyleFrozenSiblings is returned by UpdateStyle when a change to the style's SKU facts (season)
// would have to re-mint a colourway whose SKU is frozen (has order/label history, sku_locked_at set).
// The whole change is refused rather than silently skipping the frozen sibling; the official path is
// CloneStyleForSeason. The API layer maps it to FailedPrecondition (R4).
var ErrStyleFrozenSiblings = errors.New("style has SKU-frozen colourways; clone for the new season instead")

// ErrColorwayColorExists is returned by CreateColorway when the (style_id, color_code) pair already
// exists (UNIQUE, R1). The API layer maps it to FailedPrecondition.
var ErrColorwayColorExists = errors.New("a colourway with this colour already exists for the style")

// ErrColorwayNotSellable is the classification sentinel for a failed completeness gate on the →ACTIVE
// edge (publish DRAFT->ACTIVE / unhide HIDDEN->ACTIVE): an unbuilt base SKU, a variant without a valid
// SKU, an incomplete sellable style, a missing colour/country/price/default translation, no thumbnail,
// or a required selling currency absent. Every one of these is an operator-fixable precondition, so the
// API layer maps it to FailedPrecondition (HTTP 400) — NOT Internal (500). Wrap the descriptive error
// as `fmt.Errorf("%w: <detail>", ErrColorwayNotSellable, ...)` so errors.Is detects it while the detail
// stays readable.
var ErrColorwayNotSellable = errors.New("colourway is not sellable")

// ColorwayStatus is a colourway's (product row's) lifecycle state and the single authoritative source
// of lifecycle — the legacy (hidden, deleted_at) pair and the generated `status` enum are gone
// (contract decision R6). It is stored as product.lifecycle_status TINYINT UNSIGNED. The numeric
// values are wire-stable: they match the ColorwayLifecycleStatus proto enum and the DB CHECK, and the
// enum drift test locks proto-number == entity-const == DB-CHECK.
type ColorwayStatus uint8

const (
	ColorwayStatusUnknown  ColorwayStatus = 0 // never written; an unknown value read from the DB is fail-closed
	ColorwayStatusDraft    ColorwayStatus = 1 // created, not yet published; admin-visible, never on the storefront
	ColorwayStatusActive   ColorwayStatus = 2 // published and publicly visible on the storefront
	ColorwayStatusHidden   ColorwayStatus = 3 // admin-visible, temporarily hidden from the storefront
	ColorwayStatusArchived ColorwayStatus = 4 // soft-deleted (deleted_at set as the archival audit stamp); restorable to HIDDEN via 'restore'
)

// IsValid reports whether s is a writable lifecycle status (Unknown is not). A value read from the DB
// that fails this is treated fail-closed (not publicly visible, not transitionable).
func (s ColorwayStatus) IsValid() bool {
	return s >= ColorwayStatusDraft && s <= ColorwayStatusArchived
}

// String returns the lowercase wire label for the status (used by the DTO projection until the proto
// numeric enum lands with T-B).
func (s ColorwayStatus) String() string {
	switch s {
	case ColorwayStatusDraft:
		return "draft"
	case ColorwayStatusActive:
		return "active"
	case ColorwayStatusHidden:
		return "hidden"
	case ColorwayStatusArchived:
		return "archived"
	default:
		return "unknown"
	}
}

// ColorwayTransition is a named lifecycle command. Lifecycle changes ONLY through these commands — a
// colourway save (UpdateColorway) never mutates lifecycle_status (R6).
type ColorwayTransition string

const (
	ColorwayTransitionPublish ColorwayTransition = "publish" // DRAFT -> ACTIVE (store also enforces publish preconditions)
	ColorwayTransitionHide    ColorwayTransition = "hide"    // ACTIVE -> HIDDEN
	ColorwayTransitionUnhide  ColorwayTransition = "unhide"  // HIDDEN -> ACTIVE
	ColorwayTransitionArchive ColorwayTransition = "archive" // ACTIVE|HIDDEN -> ARCHIVED
	ColorwayTransitionRestore ColorwayTransition = "restore" // ARCHIVED -> HIDDEN (admin unarchive-to-hidden; clears the deleted_at tombstone)
)

// colorwayTransitionGraph is the allowed lifecycle graph. ARCHIVED is soft-terminal: its ONLY outgoing
// edge is 'restore' (ARCHIVED -> HIDDEN, admin unarchive-to-hidden) — it can never go straight back to
// ACTIVE (it must be restored to HIDDEN first, then unhidden). DRAFT can only be published; UNKNOWN has
// no edges (fail-closed).
var colorwayTransitionGraph = map[ColorwayStatus]map[ColorwayTransition]ColorwayStatus{
	ColorwayStatusDraft:    {ColorwayTransitionPublish: ColorwayStatusActive},
	ColorwayStatusActive:   {ColorwayTransitionHide: ColorwayStatusHidden, ColorwayTransitionArchive: ColorwayStatusArchived},
	ColorwayStatusHidden:   {ColorwayTransitionUnhide: ColorwayStatusActive, ColorwayTransitionArchive: ColorwayStatusArchived},
	ColorwayStatusArchived: {ColorwayTransitionRestore: ColorwayStatusHidden},
}

// NextColorwayStatus validates a lifecycle transition and returns the resulting status. It encodes the
// R6 state machine and nothing else — the store layer additionally enforces Publish preconditions and
// applies the write under the style/colourway optimistic lock. An unknown source, or a command not
// allowed from the current state (e.g. anything but 'restore' out of ARCHIVED), is rejected (fail-closed).
func NextColorwayStatus(from ColorwayStatus, t ColorwayTransition) (ColorwayStatus, error) {
	if !from.IsValid() {
		return ColorwayStatusUnknown, fmt.Errorf("colourway has unknown lifecycle status %d", uint8(from))
	}
	if to, ok := colorwayTransitionGraph[from][t]; ok {
		return to, nil
	}
	return ColorwayStatusUnknown, fmt.Errorf("lifecycle transition %q is not allowed from %s", t, from)
}

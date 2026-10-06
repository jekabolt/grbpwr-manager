package entity

import "time"

// Auto parts (Ф2, 0390): the client cuts a side's flat into numbered regions, the model groups the
// numbers into garment parts and names them. One row per (card, view, flat media, cut revision) —
// a cache: the same flat cut the same way is never paid for twice.
const (
	// DesignPartsMinRegions / DesignPartsMaxRegions — a cut the model is asked to name. M5: ONE
	// region is named too (a fine drawing whose bindings the cutter could not keep is still a part;
	// it was «pen only»); above 60 the numbers stop being readable.
	DesignPartsMinRegions = 1
	DesignPartsMaxRegions = 60
	// DesignPartsMaxAlgoRev — the client's cut revision (regions.ts REGIONS_ALGO_REV).
	DesignPartsMaxAlgoRev = 32
	// DesignPartsMaxParts — parts kept from one answer.
	DesignPartsMaxParts = 40
	// DesignPartsMaxLabelRunes / DesignPartsMaxWhyRunes — the trims of the model's words.
	DesignPartsMaxLabelRunes = 40
	DesignPartsMaxWhyRunes   = 120
	// DesignPartsUnnamed — the part every region the model left out falls into.
	DesignPartsUnnamed = "unnamed"
	// DesignPartsMaxViews — the sides one card-wide call may carry (the four cardinal views).
	DesignPartsMaxViews = 4
)

// DesignPartGroup is one garment part: its name and the region numbers it is made of (1-based).
// PartKey is the part's identity across the sides of one card-wide answer (SuggestDesignPartsCard):
// the same physical part carries the same key on every side it is seen on. Empty on the rows of the
// per-side call.
type DesignPartGroup struct {
	Label   string `json:"label"`
	Regions []int  `json:"regions"`
	PartKey string `json:"part_key,omitempty"`
}

// DesignPartSplit is one region the model says spans two parts with no seam line drawn.
type DesignPartSplit struct {
	Region int    `json:"region"`
	Why    string `json:"why"`
}

// DesignPartsSuggestion is one cached answer for one cut of one side's flat.
type DesignPartsSuggestion struct {
	Id          int
	TechCardId  int
	View        string
	BaseMediaId int
	AlgoRev     string
	Parts       []DesignPartGroup
	SplitNeeded []DesignPartSplit
	Model       string
	CreatedBy   string
	CreatedAt   time.Time
}

package product

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// SKU format (SKU redesign, tasks 01-06):
//
//	base:    {SEASON}{YY}-{MODEL:5}-{COLOR:3}      fixed 14, e.g. SS26-00021-BLK
//	variant: {base}-{SIZE_ORD:2}                   fixed 17, e.g. SS26-00021-BLK-04
//
// The builders below are PURE: all style-vs-standalone resolution, dictionary lookups and DB
// collision checks happen in the store resolver (BuildProductSKUs, task 07) which feeds these
// validated segments. Keeping the builders pure makes them fully unit-testable.

const (
	// BaseSKULen is the fixed character length of a base SKU (SS26-00021-BLK).
	BaseSKULen = 14
	// VariantSKULen is the fixed character length of a variant SKU (SS26-00021-BLK-04).
	VariantSKULen = 17

	// colorFallback is the last-resort colour segment; it is a seeded dictionary code so it is also
	// FK-valid if ever written to color_code (though the generator only ever puts it in the string).
	colorFallback = "UNK"
	// seasonFallback is used when a product/style has no valid season enum.
	seasonFallback = "SS"
)

// SKUSegments are the resolved, dictionary-checked inputs for one product's SKU. The store resolver
// (task 07) fills these from either the linked style (colourway) or the standalone product.
type SKUSegments struct {
	Season    entity.SeasonEnum // SS/FW/PF/RC; empty -> seasonFallback
	Year      int               // full year, e.g. 2026 (only the last two digits are used)
	ModelNo   int               // 1..99999
	ColorCode string            // resolved dictionary code (exactly 3 uppercase chars)
}

// BuildBaseSKU renders the fixed-14 base SKU from resolved segments.
func BuildBaseSKU(s SKUSegments) string {
	return fmt.Sprintf("%s-%s-%s",
		seasonSegment(s.Season, s.Year),
		modelSegment(s.ModelNo),
		colorSegment(s.ColorCode),
	)
}

// BuildVariantSKU appends the 2-digit size ordinal to a base SKU (fixed 17).
func BuildVariantSKU(base string, sizeOrd int) string {
	return fmt.Sprintf("%s-%s", base, sizeSegment(sizeOrd))
}

// seasonSegment is {SEASON}{YY}: a valid 2-letter season code (fallback SS) + 2-digit year.
func seasonSegment(season entity.SeasonEnum, year int) string {
	code := string(season)
	if !entity.IsValidSeason(season) {
		code = seasonFallback
	}
	yy := year % 100
	if yy < 0 {
		yy = 0
	}
	return fmt.Sprintf("%s%02d", code, yy)
}

// modelSegment is the 5-digit zero-padded model number. Numbers wider than 5 digits (>99999,
// astronomically unlikely) are rendered as-is, breaking the fixed length — the store logs on
// approach to the ceiling.
func modelSegment(modelNo int) string {
	if modelNo < 0 {
		modelNo = 0
	}
	return fmt.Sprintf("%05d", modelNo)
}

// sizeSegment is the 2-digit zero-padded size ordinal. The store refuses to build a variant SKU when
// the ordinal is 0 (unseeded size), so 0 never reaches here in practice.
func sizeSegment(ord int) string {
	if ord < 0 {
		ord = 0
	}
	return fmt.Sprintf("%02d", ord)
}

// colorSegment renders only a canonical dictionary code. Runtime resolution validates the code
// before calling the builder; UNK remains only as a deterministic guard for direct pure calls.
func colorSegment(code string) string {
	if len(code) == 3 && code == strings.ToUpper(code) {
		return code
	}
	return colorFallback
}

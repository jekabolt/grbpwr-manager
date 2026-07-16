package product

import (
	"fmt"
	"regexp"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// SKU format (SKU redesign, tasks 01-06; contract frozen as grbpwr-sku-v1, R7):
//
//	base:    {SEASON}{YY}-{MODEL:5}-{COLOR:3}      fixed 14, e.g. SS26-00021-BLK
//	variant: {base}-{SIZE_ORD:2}                   fixed 17, e.g. SS26-00021-BLK-04
//
// The builders below are PURE and STRICT: all style-vs-standalone resolution, dictionary lookups and
// DB collision checks happen in the store resolver (BuildProductSKUs, task 07) which feeds these
// validated segments; the builders additionally reject anything that would break the fixed-length
// invariant instead of silently substituting a placeholder (problem 045). A caller that must produce
// a best-effort SKU from corrupted/legacy facts (e.g. a one-off migration pass) has to opt into that
// explicitly via BuildBaseSKUPermissive/BuildVariantSKUPermissive below, which report every
// substitution instead of hiding it.
const (
	// BaseSKULen is the fixed character length of a base SKU (SS26-00021-BLK).
	BaseSKULen = 14
	// VariantSKULen is the fixed character length of a variant SKU (SS26-00021-BLK-04).
	VariantSKULen = 17

	// minSKUYear/maxSKUYear bound the 2-digit year segment (R7): only 2000..2099 renders unambiguously.
	minSKUYear = 2000
	maxSKUYear = 2099
	// minModelNo/maxModelNo bound the 5-digit model segment: 0 is not a real model, and anything past
	// 99999 would overflow the fixed width.
	minModelNo = 1
	maxModelNo = 99999
	// minSizeOrd/maxSizeOrd bound the 2-digit size-ordinal segment for the same reason.
	minSizeOrd = 1
	maxSizeOrd = 99

	// ModelNoWarnThreshold/ModelNoCriticalThreshold gate operational ceiling-alerts (R7) as model_no
	// approaches the fixed 5-digit width. Both values are still perfectly VALID model numbers — see
	// ClassifyModelNoCeiling — this is early warning for capacity planning (the next SKU contract
	// version), not a validation boundary; validation is minModelNo/maxModelNo above.
	ModelNoWarnThreshold     = 90000
	ModelNoCriticalThreshold = 99000

	// colorFallback is the last-resort colour segment used ONLY by the permissive migration-mode
	// builder; it is a seeded dictionary code so it stays FK-valid if ever written to color_code.
	colorFallback = "UNK"
	// seasonFallback is used by the permissive migration-mode builder when a product/style has no
	// valid season enum.
	seasonFallback = "SS"
)

// colorCodePattern is the canonical shape of a colour segment: exactly 3 uppercase alphanumerics
// (R7/R9). This is a FORMAT check only — dictionary membership is a store-resolver concern
// (validateColorCode in sku_resolve.go), not something the pure builder can verify.
var colorCodePattern = regexp.MustCompile(`^[A-Z0-9]{3}$`)

// SKUSegments are the resolved, dictionary-checked inputs for one product's SKU. The store resolver
// (task 07) fills these from either the linked style (colourway) or the standalone product.
type SKUSegments struct {
	Season    entity.SeasonEnum // SS/FW/PF/RC; must be canonical — no fallback in the strict builder
	Year      int               // full year, e.g. 2026; must be 2000..2099
	ModelNo   int               // must be 1..99999
	ColorCode string            // resolved dictionary code; must be exactly 3 uppercase alphanumerics
}

// ModelNoCeilingLevel classifies how close a model number is to exhausting the fixed 5-digit width
// (maxModelNo). It never invalidates the number — see ClassifyModelNoCeiling.
type ModelNoCeilingLevel int

const (
	ModelNoCeilingOK ModelNoCeilingLevel = iota
	ModelNoCeilingWarn
	ModelNoCeilingCritical
)

// ClassifyModelNoCeiling reports whether modelNo warrants an operational ceiling-alert (R7). The
// pure builder never logs (it stays testable without a logger); the store resolver calls this after
// resolving/allocating a model number and emits the slog warn/error itself.
func ClassifyModelNoCeiling(modelNo int) ModelNoCeilingLevel {
	switch {
	case modelNo >= ModelNoCriticalThreshold:
		return ModelNoCeilingCritical
	case modelNo >= ModelNoWarnThreshold:
		return ModelNoCeilingWarn
	default:
		return ModelNoCeilingOK
	}
}

// BuildBaseSKU renders the fixed-14 base SKU from resolved segments. It is STRICT (R7): an invalid
// season, an out-of-range year/model, or a color code that is not exactly [A-Z0-9]{3} is an error,
// never a silently-substituted placeholder — a corrupted/unknown fact must stop the mint rather than
// produce a plausible-looking but wrong SKU.
func BuildBaseSKU(s SKUSegments) (string, error) {
	season, err := strictSeasonSegment(s.Season, s.Year)
	if err != nil {
		return "", fmt.Errorf("sku: base: %w", err)
	}
	model, err := strictModelSegment(s.ModelNo)
	if err != nil {
		return "", fmt.Errorf("sku: base: %w", err)
	}
	color, err := strictColorSegment(s.ColorCode)
	if err != nil {
		return "", fmt.Errorf("sku: base: %w", err)
	}
	return fmt.Sprintf("%s-%s-%s", season, model, color), nil
}

// BuildVariantSKU appends the 2-digit size ordinal to a base SKU (fixed 17). It is STRICT: base must
// already be a canonical fixed-14 base SKU and sizeOrd must be a valid 1..99 ordinal (an unknown/unseeded
// size ordinal or one outside the 2-digit width is an error, not a truncated/blank segment).
func BuildVariantSKU(base string, sizeOrd int) (string, error) {
	if len(base) != BaseSKULen {
		return "", fmt.Errorf("sku: variant: base %q is not a canonical %d-char base SKU", base, BaseSKULen)
	}
	size, err := strictSizeSegment(sizeOrd)
	if err != nil {
		return "", fmt.Errorf("sku: variant: %w", err)
	}
	return fmt.Sprintf("%s-%s", base, size), nil
}

// strictSeasonSegment renders {SEASON}{YY}, rejecting an unknown season (unknown mapping) or a year
// outside 2000..2099.
func strictSeasonSegment(season entity.SeasonEnum, year int) (string, error) {
	if !entity.IsValidSeason(season) {
		return "", fmt.Errorf("season %q is not a canonical SKU season (unknown mapping)", season)
	}
	if year < minSKUYear || year > maxSKUYear {
		return "", fmt.Errorf("season year %d must be between %d and %d", year, minSKUYear, maxSKUYear)
	}
	return fmt.Sprintf("%s%02d", season, year%100), nil
}

// strictModelSegment renders the 5-digit zero-padded model number, rejecting 0 or anything wider
// than 5 digits.
func strictModelSegment(modelNo int) (string, error) {
	if modelNo < minModelNo || modelNo > maxModelNo {
		return "", fmt.Errorf("model number %d must be between %d and %d", modelNo, minModelNo, maxModelNo)
	}
	return fmt.Sprintf("%05d", modelNo), nil
}

// strictColorSegment renders the 3-char colour code, rejecting anything that is not exactly
// [A-Z0-9]{3} (dictionary membership is checked upstream by the store resolver).
func strictColorSegment(code string) (string, error) {
	if !colorCodePattern.MatchString(code) {
		return "", fmt.Errorf("color code %q is not canonical (must match [A-Z0-9]{3})", code)
	}
	return code, nil
}

// strictSizeSegment renders the 2-digit zero-padded size ordinal, rejecting 0 (unseeded size) or
// anything past the 2-digit width.
func strictSizeSegment(ord int) (string, error) {
	if ord < minSizeOrd || ord > maxSizeOrd {
		return "", fmt.Errorf("size ordinal %d must be between %d and %d", ord, minSizeOrd, maxSizeOrd)
	}
	return fmt.Sprintf("%02d", ord), nil
}

// FallbackReport records which segments of a permissively-built SKU were substituted with a
// deterministic placeholder because the input fact was invalid, out of range or missing. A caller
// MUST inspect/log a report with Applied() true — a placeholder identity must never be mistaken for
// a validated one.
type FallbackReport struct {
	SeasonFallback   bool // season/year invalid -> seasonFallback ("SS") + year clamped into range
	ModelFallback    bool // model number out of 1..99999 -> clamped to the nearest bound
	ColorFallback    bool // color code not canonical [A-Z0-9]{3} -> colorFallback ("UNK")
	OrdinalFallback  bool // size ordinal out of 1..99 -> clamped to the nearest bound
	BaseShapeInvalid bool // (variant only) base was not a canonical fixed-14 base SKU
}

// Applied reports whether BuildBaseSKUPermissive/BuildVariantSKUPermissive substituted at least one
// placeholder segment.
func (r FallbackReport) Applied() bool {
	return r.SeasonFallback || r.ModelFallback || r.ColorFallback || r.OrdinalFallback || r.BaseShapeInvalid
}

// BuildBaseSKUPermissive is the explicit migration-mode counterpart to BuildBaseSKU (R7): unlike the
// strict builder it never errors, substituting a deterministic placeholder for any invalid segment so
// a legacy-data reconciliation pass can still produce a fixed-length string to work from. It returns a
// FallbackReport recording every substitution — the caller must not treat the result as a validated
// identity without inspecting it (e.g. surfacing Applied() rows for manual review, the way
// skuBackfillReadinessError aggregates mint failures).
func BuildBaseSKUPermissive(s SKUSegments) (string, FallbackReport) {
	var report FallbackReport

	season := string(s.Season)
	if !entity.IsValidSeason(s.Season) {
		season = seasonFallback
		report.SeasonFallback = true
	}
	yy := s.Year % 100
	if yy < 0 {
		yy = 0
		report.SeasonFallback = true
	}
	seasonSeg := fmt.Sprintf("%s%02d", season, yy)

	modelNo := s.ModelNo
	switch {
	case modelNo < minModelNo:
		modelNo = minModelNo
		report.ModelFallback = true
	case modelNo > maxModelNo:
		modelNo = maxModelNo
		report.ModelFallback = true
	}
	modelSeg := fmt.Sprintf("%05d", modelNo)

	color := s.ColorCode
	if !colorCodePattern.MatchString(color) {
		color = colorFallback
		report.ColorFallback = true
	}

	return fmt.Sprintf("%s-%s-%s", seasonSeg, modelSeg, color), report
}

// BuildVariantSKUPermissive is the explicit migration-mode counterpart to BuildVariantSKU (R7): it
// never errors, clamping an out-of-range ordinal to the nearest bound and flagging a non-canonical
// base shape instead of rejecting. See BuildBaseSKUPermissive for the reporting contract.
func BuildVariantSKUPermissive(base string, sizeOrd int) (string, FallbackReport) {
	var report FallbackReport
	if len(base) != BaseSKULen {
		report.BaseShapeInvalid = true
	}

	ord := sizeOrd
	switch {
	case ord < minSizeOrd:
		ord = minSizeOrd
		report.OrdinalFallback = true
	case ord > maxSizeOrd:
		ord = maxSizeOrd
		report.OrdinalFallback = true
	}

	return fmt.Sprintf("%s-%02d", base, ord), report
}

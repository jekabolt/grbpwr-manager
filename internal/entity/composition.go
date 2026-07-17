package entity

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// CompositionSourceAuto / CompositionSourceManual are the style_composition provenance values: an
// auto composition is re-derived from the BOM on every save; a manual override is authored by the
// owner and is NEVER overwritten by the auto derivation (S17 defect 2).
const (
	CompositionSourceAuto   = "auto"
	CompositionSourceManual = "manual"
)

// compositionTotalTolerance bounds how far a derived total may sit from 100 before it is rejected as
// bad input (each contributing fabric should itself sum to 100). It absorbs only rounding drift; a
// genuinely unbalanced fabric composition (e.g. one summing to 50) exceeds it and is field-tagged.
var compositionTotalTolerance = decimal.NewFromInt(1)

// FiberPercent is one fibre's share of a composition (percent in [0,100]).
type FiberPercent struct {
	FiberCode string
	Percent   decimal.Decimal
}

// CompositionEntry is one fibre share of a style's structured composition, resolved with its
// dictionary display name (S17) — the shared shape for BOTH read paths that project
// style_composition: the style/tech-card read (internal/store/techcard/composition_read.go) and the
// colourway/storefront read (internal/store/product/query.go, JSON_ARRAYAGG'd in SQL under the same
// field names below). M1 fix: this is the TYPED wire projection — composition (the free-text legacy
// column) is never overloaded with it; a client renders CompositionEntries when present and falls
// back to the plain-text composition otherwise.
type CompositionEntry struct {
	FiberCode string          `db:"fiber_code" json:"fiber_code"`
	Name      string          `db:"name" json:"name"`
	Percent   decimal.Decimal `db:"percent" json:"percent"`
}

// materialCompositionTolerance bounds how far a directly-authored material composition may sum from
// 100. Unlike a BOM-derived style composition (compositionTotalTolerance, a 1-point cross-fabric
// rounding budget), a catalog material's fibre breakdown is entered as one balanced set, so the
// tolerance is a tight rounding epsilon.
var materialCompositionTolerance = decimal.New(1, -2) // 0.01

// NormalizeMaterialComposition validates and canonicalises a catalog material's structural fibre
// composition (S17, material_composition). Fibre codes are upper-cased/trimmed; each percent must be
// in [0,100]; a fibre may appear at most once; and a non-empty set must sum to 100 (± a rounding
// epsilon). An empty input is a valid "no composition" and returns (nil, nil). Errors are field-tagged
// (NewFieldViolation) so the API binds the failure to the offending entry. It does NOT check that a
// fibre code exists in the dictionary — that FK check is the store's (checkFibersExist).
func NormalizeMaterialComposition(entries []CompositionEntry) ([]CompositionEntry, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	hundred := decimal.NewFromInt(100)
	out := make([]CompositionEntry, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	total := decimal.Zero
	for i := range entries {
		code := strings.ToUpper(strings.TrimSpace(entries[i].FiberCode))
		if code == "" {
			return nil, NewFieldViolation(fmt.Sprintf("composition_entries[%d].fiber_code", i),
				"fibre code is required", "", "reference a fibre from the dictionary")
		}
		if seen[code] {
			return nil, NewFieldViolation(fmt.Sprintf("composition_entries[%d].fiber_code", i),
				fmt.Sprintf("fibre %s appears more than once", code), "", "list each fibre at most once")
		}
		seen[code] = true
		p := entries[i].Percent
		if p.IsNegative() || p.GreaterThan(hundred) {
			return nil, NewFieldViolation(fmt.Sprintf("composition_entries[%d].percent", i),
				fmt.Sprintf("fibre %s percent %s is out of range", code, p.String()), "",
				"percentages must be between 0 and 100")
		}
		total = total.Add(p)
		out = append(out, CompositionEntry{FiberCode: code, Percent: p})
	}
	if total.Sub(hundred).Abs().GreaterThan(materialCompositionTolerance) {
		return nil, NewFieldViolation("composition_entries",
			fmt.Sprintf("fibre percentages total %s, not 100", total.String()), "",
			"a material's fibre composition must sum to 100%")
	}
	return out, nil
}

// DeriveStyleComposition aggregates the shell-fabric BOM lines' fibre compositions into the garment
// composition (source=auto, S17 / acceptance C.11):
//
//   - a single fabric contributes its composition at 100%;
//   - N fabrics divide equally — each weighted 1/N (owner's rule; consumption-weighting is a
//     documented follow-up, not this wave);
//   - a fibre appearing in several fabrics has its shares summed (deduped);
//   - each result rounds to 2 decimals and the set is nudged so the total is exactly 100.
//
// An empty input yields an empty composition (a style with no shell fabric has no derived
// composition). A derived total more than the tolerance away from 100 (a fabric that does not itself
// sum to 100) is a field-tagged error rather than a silently nudged wrong number.
func DeriveStyleComposition(fabrics [][]FiberPercent) ([]FiberPercent, error) {
	if len(fabrics) == 0 {
		return nil, nil
	}
	n := decimal.NewFromInt(int64(len(fabrics)))
	sum := make(map[string]decimal.Decimal)
	order := make([]string, 0) // stable fibre order by first appearance (deterministic output)
	for _, fab := range fabrics {
		for _, fp := range fab {
			code := strings.ToUpper(strings.TrimSpace(fp.FiberCode))
			if code == "" {
				continue
			}
			if fp.Percent.IsNegative() {
				return nil, NewFieldViolation("composition",
					fmt.Sprintf("fibre %s has a negative percent", code), "", "percentages must be between 0 and 100")
			}
			if _, seen := sum[code]; !seen {
				order = append(order, code)
			}
			sum[code] = sum[code].Add(fp.Percent.Div(n)) // 1/N equal weighting
		}
	}

	out := make([]FiberPercent, 0, len(order))
	total := decimal.Zero
	for _, code := range order {
		p := sum[code].Round(2)
		out = append(out, FiberPercent{FiberCode: code, Percent: p})
		total = total.Add(p)
	}

	hundred := decimal.NewFromInt(100)
	diff := hundred.Sub(total)
	if diff.Abs().GreaterThan(compositionTotalTolerance) {
		return nil, NewFieldViolation("composition",
			fmt.Sprintf("derived composition totals %s, not 100", total.String()), "",
			"check that each fabric's fibre composition sums to 100")
	}
	// Absorb the residual rounding drift into the largest component so the set totals exactly 100.
	if !diff.IsZero() && len(out) > 0 {
		largest := 0
		for i := range out {
			if out[i].Percent.GreaterThan(out[largest].Percent) {
				largest = i
			}
		}
		out[largest].Percent = out[largest].Percent.Add(diff)
	}
	return out, nil
}

// ReconcileStyleComposition decides what to persist for a style's composition: a manual override is
// returned untouched (auto NEVER overwrites manual, S17), otherwise the freshly derived auto set.
func ReconcileStyleComposition(currentSource string, currentManual, derived []FiberPercent) (source string, rows []FiberPercent) {
	if currentSource == CompositionSourceManual {
		return CompositionSourceManual, currentManual
	}
	return CompositionSourceAuto, derived
}

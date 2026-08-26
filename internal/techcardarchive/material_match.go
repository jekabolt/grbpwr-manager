package techcardarchive

import (
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ─────────────────────────────────────────────────────────────────────────────
// MATCHING A MATERIAL PASSPORT AGAINST A CATALOGUE — THE PURE HALF.
//
// A passport (materials/index.json, FORMAT.md §5.4) describes an article well enough to FIND the
// same one in another catalogue and not well enough to price it. The import MATCHES, it never
// CREATES: a card whose article is missing here lands with the BOM line's own name / supplier /
// unit and no catalogue link, plus a line in the report. That is the owner's rule for the whole
// feature — a gap is a SKIP WITH A REPORT, never a refusal of the import.
//
// This file is deliberately free of the database and of the API layer: it takes a passport and a
// slice of catalogue rows and answers. Ф2.3 (import resolver) and Ф6.2 (apply colourways from an
// archive) both need the same answer, and two implementations of "is this the same article" would
// eventually disagree about one — at which point the same archive would link a BOM line and refuse
// its own colourway pin.
//
// THIS IS THE ONE FILE OF THE PACKAGE THAT IMPORTS internal/entity. It is not a cycle and does not
// endanger the aliasing note in format.go (internal/bucket imports this package): entity's only
// internal dependency is internal/currency, so the arrow bucket → techcardarchive → entity is a
// straight line. entity is imported rather than re-typed because the two things needed here — the
// catalogue row and entity.SameMaterialUnit — ARE the server's answers to "what is an article" and
// "are two units the same unit", and a private copy of either would be a second answer that drifts.
// ─────────────────────────────────────────────────────────────────────────────

// MaterialVerdict is what a match attempt concluded. Four outcomes and not a bool, because three of
// them are DIFFERENT holes with different instructions for the operator: "create the article",
// "archive the duplicates", "fix the unit". Collapsing them into "not linked" would send everybody
// to the same wrong place.
type MaterialVerdict string

const (
	// MaterialMatched — exactly one live article is the same article, and the returned id is it.
	MaterialMatched MaterialVerdict = "matched"
	// MaterialNotFound — nothing in the catalogue answers to this passport.
	MaterialNotFound MaterialVerdict = "not_found"
	// MaterialAmbiguous — several LIVE articles carry the key, so none is picked. The code is unique
	// only among non-archived rows and only in the application (checkMaterialCodeFree,
	// internal/store/techcard/material_catalog.go); the schema does not enforce it, which is why two
	// live matches are a refusal to choose rather than a coin toss.
	MaterialAmbiguous MaterialVerdict = "ambiguous"
	// MaterialUnitMismatch — the key matched and the UNIT did not, so it is not the same article. A
	// material with stock movements has its unit locked (checkMaterialUnitChange), so a differing
	// unit is a statement about the article, not a spelling: linking anyway would make the card's
	// norm and the warehouse's stock two numbers in two units added together.
	MaterialUnitMismatch MaterialVerdict = "unit_mismatch"
)

// ReasonForMaterialVerdict maps a verdict onto the closed hole dictionary, and returns an empty
// Reason for MaterialMatched — a success is not a hole and writes no line.
//
// It lives here, next to the verdicts, so that a caller cannot pick the wrong code: Ф2.3 and Ф6.2
// both report these, and "ambiguous reported as not_found" would tell an operator to create an
// article that already exists twice.
func ReasonForMaterialVerdict(v MaterialVerdict) Reason {
	switch v {
	case MaterialNotFound:
		return ReasonMaterialNotFound
	case MaterialAmbiguous:
		return ReasonMaterialAmbiguous
	case MaterialUnitMismatch:
		return ReasonMaterialUnitMismatch
	default:
		return ""
	}
}

// MatchMaterial finds the target-catalogue article a passport describes.
//
// The ladder is FORMAT.md §5.4, in this order and no other:
//
//  1. `code` among NON-ARCHIVED rows. The code is our own article number and the strongest key
//     there is — but only among live rows, so archived rows are not even candidates. One candidate
//     → check the unit. Several → MaterialAmbiguous, and the ladder STOPS: a supplier-ref fallback
//     after an ambiguous code would quietly pick one of the two articles the code refused to
//     choose between.
//  2. `(supplier, supplier_ref)` among non-archived rows, both halves non-empty. Same rules,
//     including the unit check: the supplier's own article number is as much an identity as ours.
//  3. Otherwise MaterialNotFound.
//
// A UNIT CHECK ONLY FIRES WHEN BOTH SIDES CARRY A UNIT. A mismatch is two values that disagree;
// a value against a blank is a blank, and refusing to link an article because the archive's row
// never had a unit typed on it would turn missing data into a contradiction. entity.SameMaterialUnit
// is what decides, so «м» and "m" are the same unit here exactly as they are on every write path.
//
// Returns (0, verdict) for everything except MaterialMatched. The catalogue row's own id is an int
// and the BOM line's material_id is an int64; the widening happens here so no caller repeats it.
func MatchMaterial(p MaterialPassport, catalog []entity.Material) (int64, MaterialVerdict) {
	if code := strings.TrimSpace(p.Code); code != "" {
		matches := matchMaterialsBy(catalog, func(m *entity.Material) bool {
			return sameMaterialKey(m.Code.String, code)
		})
		if v, ok := decideMaterialMatch(p, matches); ok {
			return v.id, v.verdict
		}
	}

	supplier, ref := strings.TrimSpace(p.Supplier), strings.TrimSpace(p.SupplierRef)
	if supplier != "" && ref != "" {
		matches := matchMaterialsBy(catalog, func(m *entity.Material) bool {
			return sameMaterialKey(m.Supplier.String, supplier) && sameMaterialKey(m.SupplierRef.String, ref)
		})
		if v, ok := decideMaterialMatch(p, matches); ok {
			return v.id, v.verdict
		}
	}

	return 0, MaterialNotFound
}

// materialMatchResult is one rung's answer.
type materialMatchResult struct {
	id      int64
	verdict MaterialVerdict
}

// decideMaterialMatch turns a rung's candidate set into a verdict. ok=false means «this rung said
// nothing» (no candidate at all) and the ladder continues; ok=true means the rung DECIDED — matched,
// ambiguous or unit-mismatched — and the ladder stops there.
func decideMaterialMatch(p MaterialPassport, matches []*entity.Material) (materialMatchResult, bool) {
	switch len(matches) {
	case 0:
		return materialMatchResult{}, false
	case 1:
		m := matches[0]
		if materialUnitsDisagree(p.Unit, m.Unit.String) {
			return materialMatchResult{verdict: MaterialUnitMismatch}, true
		}
		return materialMatchResult{id: int64(m.Id), verdict: MaterialMatched}, true
	default:
		return materialMatchResult{verdict: MaterialAmbiguous}, true
	}
}

// matchMaterialsBy collects the LIVE catalogue rows a predicate accepts. Archived rows are skipped
// before the predicate runs, which is what makes «unique among non-archived» the rule here too.
func matchMaterialsBy(catalog []entity.Material, pred func(*entity.Material) bool) []*entity.Material {
	var out []*entity.Material
	for i := range catalog {
		m := &catalog[i]
		if m.Archived {
			continue
		}
		if pred(m) {
			out = append(out, m)
		}
	}
	return out
}

// sameMaterialKey compares two catalogue keys the way the DATABASE compares them: trimmed and
// case-insensitively. MySQL's default collation on these columns is case-insensitive, so
// checkMaterialCodeFree already treats "F-WOOL-320" and "f-wool-320" as one code — a case-sensitive
// comparison here would call two rows distinct that the uniqueness check calls duplicates, and pick
// one of a pair it had no right to choose between. b is expected pre-trimmed.
func sameMaterialKey(a, b string) bool {
	a = strings.TrimSpace(a)
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(a, b)
}

// materialUnitsDisagree reports a real contradiction and nothing else: two units, both stated, that
// are not the same unit. Either side blank is «nothing was claimed», never a mismatch.
func materialUnitsDisagree(passportUnit, catalogUnit string) bool {
	a, b := strings.TrimSpace(passportUnit), strings.TrimSpace(catalogUnit)
	if a == "" || b == "" {
		return false
	}
	return !entity.SameMaterialUnit(a, b)
}

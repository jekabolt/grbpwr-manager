package techcardarchive

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// TestMatchMaterial* — the ladder of FORMAT.md §5.4, rung by rung.
//
// Every case below is a state a REAL catalogue reaches: two live rows sharing a code because the
// schema never forbade it, an archived predecessor of a re-issued article, an article whose unit
// was corrected on one instance and not the other. The point of the file is that each of them
// produces a DIFFERENT verdict — the three failure verdicts carry three different instructions for
// the operator, and a test that only checked "not matched" would let them collapse into one.
// ─────────────────────────────────────────────────────────────────────────────

func mmNS(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

// mmMaterial builds a catalogue row. Everything the matcher never reads (prices, attributes,
// composition) is absent by construction — the matcher takes entity.Material and not
// MaterialWithPrice precisely so that a price cannot be reached from here.
func mmMaterial(id int, code, supplier, ref, unit string) entity.Material {
	m := entity.Material{Id: id}
	m.Name = "row " + code
	if code != "" {
		m.Code = mmNS(code)
	}
	if supplier != "" {
		m.Supplier = mmNS(supplier)
	}
	if ref != "" {
		m.SupplierRef = mmNS(ref)
	}
	if unit != "" {
		m.Unit = mmNS(unit)
	}
	return m
}

func mmArchived(m entity.Material) entity.Material {
	m.Archived = true
	return m
}

// The happy rung: one live article carries the code, the units agree, the id comes back.
func TestMatchMaterialByCode(t *testing.T) {
	catalog := []entity.Material{
		mmMaterial(11, "F-COTTON-160", "Other", "OC-1", "m"),
		mmMaterial(12, "F-WOOL-320", "Lanificio", "LM-320-BLK", "m"),
	}
	id, verdict := MatchMaterial(MaterialPassport{Ref: 8120, Code: "F-WOOL-320", Unit: "m"}, catalog)
	require.Equal(t, MaterialMatched, verdict)
	require.Equal(t, int64(12), id)
}

// The unit vocabulary decides, not the spelling: «м» and "m" are one unit on every write path of
// this codebase (entity.SameMaterialUnit) and must be one unit here too, or a Cyrillic catalogue
// would refuse every archive written against a canonicalised one.
func TestMatchMaterialUnitSynonymIsNotAMismatch(t *testing.T) {
	catalog := []entity.Material{mmMaterial(12, "F-WOOL-320", "", "", "м")}
	id, verdict := MatchMaterial(MaterialPassport{Code: "F-WOOL-320", Unit: "m"}, catalog)
	require.Equal(t, MaterialMatched, verdict)
	require.Equal(t, int64(12), id)
}

// The code matched and the unit did not. NOT LINKED, and with its own verdict: a metre-priced roll
// and a kilo-priced one are two articles, and the card's norm would be added to the warehouse's
// stock in the wrong unit.
func TestMatchMaterialUnitMismatchDoesNotLink(t *testing.T) {
	catalog := []entity.Material{mmMaterial(12, "F-WOOL-320", "Lanificio", "LM-320-BLK", "kg")}
	id, verdict := MatchMaterial(MaterialPassport{Code: "F-WOOL-320", Unit: "m"}, catalog)
	require.Equal(t, MaterialUnitMismatch, verdict)
	require.Zero(t, id, "a unit mismatch must not fall through to a link")
	require.Equal(t, ReasonMaterialUnitMismatch, ReasonForMaterialVerdict(verdict))
}

// A unit mismatch on the code rung STOPS the ladder — it does not fall through to the supplier pair
// and link the very row it just refused.
func TestMatchMaterialUnitMismatchDoesNotFallThroughToSupplier(t *testing.T) {
	catalog := []entity.Material{mmMaterial(12, "F-WOOL-320", "Lanificio", "LM-320-BLK", "kg")}
	id, verdict := MatchMaterial(MaterialPassport{
		Code: "F-WOOL-320", Supplier: "Lanificio", SupplierRef: "LM-320-BLK", Unit: "m",
	}, catalog)
	require.Equal(t, MaterialUnitMismatch, verdict)
	require.Zero(t, id)
}

// Either side blank is «nothing was claimed», never a contradiction. A passport with no unit must
// still link, or missing data would masquerade as a disagreement.
func TestMatchMaterialBlankUnitIsNotAMismatch(t *testing.T) {
	catalog := []entity.Material{mmMaterial(12, "F-WOOL-320", "", "", "m")}

	id, verdict := MatchMaterial(MaterialPassport{Code: "F-WOOL-320"}, catalog)
	require.Equal(t, MaterialMatched, verdict)
	require.Equal(t, int64(12), id)

	catalog[0].Unit = sql.NullString{}
	id, verdict = MatchMaterial(MaterialPassport{Code: "F-WOOL-320", Unit: "m"}, catalog)
	require.Equal(t, MaterialMatched, verdict)
	require.Equal(t, int64(12), id)
}

// Two LIVE rows carry the code. The schema does not enforce uniqueness (0106:79) — only the
// application does, and only among non-archived rows — so this state exists in real bases and the
// matcher refuses to choose.
func TestMatchMaterialAmbiguousCodeDoesNotLink(t *testing.T) {
	catalog := []entity.Material{
		mmMaterial(12, "F-WOOL-320", "Lanificio", "LM-320-BLK", "m"),
		mmMaterial(13, "F-WOOL-320", "Другой", "X-1", "m"),
	}
	id, verdict := MatchMaterial(MaterialPassport{Code: "F-WOOL-320", Unit: "m"}, catalog)
	require.Equal(t, MaterialAmbiguous, verdict)
	require.Zero(t, id, "ambiguity must not resolve into the first row")
	require.Equal(t, ReasonMaterialAmbiguous, ReasonForMaterialVerdict(verdict))
}

// An ambiguous code does not fall through to the supplier pair either: picking there would decide
// exactly the question the code rung declined to answer.
func TestMatchMaterialAmbiguousCodeStopsTheLadder(t *testing.T) {
	catalog := []entity.Material{
		mmMaterial(12, "F-WOOL-320", "Lanificio", "LM-320-BLK", "m"),
		mmMaterial(13, "F-WOOL-320", "Other", "X-1", "m"),
	}
	id, verdict := MatchMaterial(MaterialPassport{
		Code: "F-WOOL-320", Supplier: "Lanificio", SupplierRef: "LM-320-BLK", Unit: "m",
	}, catalog)
	require.Equal(t, MaterialAmbiguous, verdict)
	require.Zero(t, id)
}

// Archived rows are not candidates: the code's uniqueness is a promise about live rows only, so an
// archived predecessor sharing the code must neither match nor make its live successor ambiguous.
func TestMatchMaterialIgnoresArchivedRows(t *testing.T) {
	catalog := []entity.Material{
		mmArchived(mmMaterial(9, "F-WOOL-320", "Lanificio", "LM-320-BLK", "m")),
		mmMaterial(12, "F-WOOL-320", "Lanificio", "LM-320-BLK", "m"),
	}
	id, verdict := MatchMaterial(MaterialPassport{Code: "F-WOOL-320", Unit: "m"}, catalog)
	require.Equal(t, MaterialMatched, verdict)
	require.Equal(t, int64(12), id)

	// And with ONLY the archived row left there is nothing to link to at all.
	id, verdict = MatchMaterial(MaterialPassport{Code: "F-WOOL-320", Unit: "m"}, catalog[:1])
	require.Equal(t, MaterialNotFound, verdict)
	require.Zero(t, id)
	require.Equal(t, ReasonMaterialNotFound, ReasonForMaterialVerdict(verdict))
}

// The second rung: no code (or a code nobody here uses) still finds the article by the SUPPLIER's
// own number for it.
func TestMatchMaterialBySupplierPair(t *testing.T) {
	catalog := []entity.Material{mmMaterial(12, "OTHER-CODE", "Lanificio", "LM-320-BLK", "m")}

	id, verdict := MatchMaterial(MaterialPassport{
		Supplier: "lanificio", SupplierRef: "lm-320-blk", Unit: "m",
	}, catalog)
	require.Equal(t, MaterialMatched, verdict, "supplier keys compare the way the database compares them")
	require.Equal(t, int64(12), id)

	// A code that matches nothing falls through to the pair rather than ending the ladder.
	id, verdict = MatchMaterial(MaterialPassport{
		Code: "NOT-IN-THIS-BASE", Supplier: "Lanificio", SupplierRef: "LM-320-BLK", Unit: "m",
	}, catalog)
	require.Equal(t, MaterialMatched, verdict)
	require.Equal(t, int64(12), id)
}

// HALF a pair is not a key. A supplier with no ref would match every article that supplier sells.
func TestMatchMaterialHalfASupplierPairIsNotAKey(t *testing.T) {
	catalog := []entity.Material{mmMaterial(12, "", "Lanificio", "LM-320-BLK", "m")}

	_, verdict := MatchMaterial(MaterialPassport{Supplier: "Lanificio", Unit: "m"}, catalog)
	require.Equal(t, MaterialNotFound, verdict)

	_, verdict = MatchMaterial(MaterialPassport{SupplierRef: "LM-320-BLK", Unit: "m"}, catalog)
	require.Equal(t, MaterialNotFound, verdict)
}

// Two live rows under one supplier pair are as ambiguous as two under one code.
func TestMatchMaterialAmbiguousSupplierPair(t *testing.T) {
	catalog := []entity.Material{
		mmMaterial(12, "", "Lanificio", "LM-320-BLK", "m"),
		mmMaterial(13, "", "Lanificio", "LM-320-BLK", "m"),
	}
	id, verdict := MatchMaterial(MaterialPassport{Supplier: "Lanificio", SupplierRef: "LM-320-BLK"}, catalog)
	require.Equal(t, MaterialAmbiguous, verdict)
	require.Zero(t, id)
}

// A passport with no key at all, and an empty catalogue: both are «nothing to match», not a panic
// and not a link to row zero.
func TestMatchMaterialWithNothingToMatchOn(t *testing.T) {
	id, verdict := MatchMaterial(MaterialPassport{Ref: 8120, Name: "wool melton"}, []entity.Material{
		mmMaterial(12, "F-WOOL-320", "Lanificio", "LM-320-BLK", "m"),
	})
	require.Equal(t, MaterialNotFound, verdict)
	require.Zero(t, id)

	id, verdict = MatchMaterial(MaterialPassport{Code: "F-WOOL-320"}, nil)
	require.Equal(t, MaterialNotFound, verdict)
	require.Zero(t, id)
}

// A matched verdict is NOT a hole: ReasonForMaterialVerdict must stay empty for it, so a caller
// that reports every verdict cannot manufacture a report line for a success.
func TestReasonForMaterialVerdictIsEmptyForASuccess(t *testing.T) {
	require.Equal(t, Reason(""), ReasonForMaterialVerdict(MaterialMatched))
	require.Equal(t, Reason(""), ReasonForMaterialVerdict(MaterialVerdict("something else")))
}

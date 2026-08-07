package admin

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// КАК КРОИТСЯ (0275) is `optional` so a tab holding an older bundle cannot erase a marking it cannot
// speak. That fixes the WRITE and, on its own, breaks the DIGEST — exactly as it did for направление
// ткани before it: the field sits in constructionProjection, whose invariant is that it hashes only
// what survives the store round-trip unchanged, and an omitted field arrives as an empty NullString
// while the column keeps `mirrored`. A CONSTRUCTION approval made from precisely the client the
// optionality exists for would then read «changed since sign-off» the instant it was made — and
// forever, because re-approving from the same client hashes the same absence.
//
// Three places are required and this is the third: `optional` in the proto, IF(:cut_symmetry_omitted,
// …) in the store, and this carry before the digest is restamped. Missing this one is not a visible
// bug; it is a sign-off born stale.
func TestCarryOmittedPieceCutSymmetryKeepsTheConstructionDigestStable(t *testing.T) {
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	const (
		front = "01DGSTPIECEFRONT0000000F1"
		back  = "01DGSTPIECEBACK00000000B1"
	)
	stored := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		Pieces: []entity.TechCardPiece{
			{LineKey: front, Name: "полочка", PiecesPerGarment: 2, Grainline: "lengthwise", CutSymmetry: ns("mirrored")},
			{LineKey: back, Name: "спинка", PiecesPerGarment: 1, Grainline: "lengthwise", CutSymmetry: ns("fold")},
		},
	}}
	// What the STORED card fingerprints to — the value a fresh approval must match.
	want := dto.TechCardSectionDigests(&stored.TechCardInsert)[entity.SignoffConstruction]

	// A stale tab: identical content, but it does not speak the field at all.
	staleTab := &entity.TechCardInsert{Pieces: []entity.TechCardPiece{
		{LineKey: front, Name: "полочка", PiecesPerGarment: 2, Grainline: "lengthwise", CutSymmetryOmitted: true},
		{LineKey: back, Name: "спинка", PiecesPerGarment: 1, Grainline: "lengthwise", CutSymmetryOmitted: true},
	}}
	require.NotEqual(t, want, dto.TechCardSectionDigests(staleTab)[entity.SignoffConstruction],
		"guard: without the carry the digests must differ, or this test proves nothing")

	carryOmittedPieceCutSymmetryFrom(stored, staleTab)
	require.Equal(t, want, dto.TechCardSectionDigests(staleTab)[entity.SignoffConstruction],
		"an approval from a tab that cannot speak the field must not be born stale")

	// The carry is not a blanket copy. A tab that DID speak still owns the value, including when it
	// deliberately clears one — otherwise the marking would become unclearable and «не размечено»
	// would be a state you could enter but never return to.
	current := &entity.TechCardInsert{Pieces: []entity.TechCardPiece{
		{LineKey: front, Name: "полочка", PiecesPerGarment: 2, Grainline: "lengthwise", CutSymmetry: ns("identical")},
		{LineKey: back, Name: "спинка", PiecesPerGarment: 1, Grainline: "lengthwise"},
	}}
	carryOmittedPieceCutSymmetryFrom(stored, current)
	require.Equal(t, "identical", current.Pieces[0].CutSymmetry.String)
	require.False(t, current.Pieces[1].CutSymmetry.Valid, "an explicit clear survives the carry")

	// A piece the stored card does not have (a newly added row) is left alone rather than matched by
	// position: guessing would attach one piece's pairing to another's, and pairing is exactly the
	// fact that must never be inferred from a neighbour.
	fresh := &entity.TechCardInsert{Pieces: []entity.TechCardPiece{
		{LineKey: "01DGSTPIECENEW000000000N1", Name: "манжета", PiecesPerGarment: 2, CutSymmetryOmitted: true},
	}}
	carryOmittedPieceCutSymmetryFrom(stored, fresh)
	require.False(t, fresh.Pieces[0].CutSymmetry.Valid, "a new piece has nothing stored to carry")

	// Nil ends are a no-op, not a panic: the create path has no stored card at all.
	carryOmittedPieceCutSymmetryFrom(nil, fresh)
	carryOmittedPieceCutSymmetryFrom(stored, nil)
}

// Keyed matching must agree with the STORE's own keying, which is exact after trimming. A key that
// differs only in case is a row the store will INSERT as new, so folding case here would hand a
// brand-new piece the pairing of a different one — silently, and onto the signed digest.
func TestCarryOmittedPieceCutSymmetryMatchesTheStoresKeying(t *testing.T) {
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	stored := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		Pieces: []entity.TechCardPiece{
			{LineKey: "01ABCDEF0000000000000001", Name: "полочка", PiecesPerGarment: 2, CutSymmetry: ns("mirrored")},
		},
	}}

	sameKeyPadded := &entity.TechCardInsert{Pieces: []entity.TechCardPiece{
		{LineKey: "  01ABCDEF0000000000000001  ", Name: "полочка", PiecesPerGarment: 2, CutSymmetryOmitted: true},
	}}
	carryOmittedPieceCutSymmetryFrom(stored, sameKeyPadded)
	require.Equal(t, "mirrored", sameKeyPadded.Pieces[0].CutSymmetry.String,
		"the store trims the incoming key, so the carry must trim it too")

	differentCase := &entity.TechCardInsert{Pieces: []entity.TechCardPiece{
		{LineKey: "01abcdef0000000000000001", Name: "полочка", PiecesPerGarment: 2, CutSymmetryOmitted: true},
	}}
	carryOmittedPieceCutSymmetryFrom(stored, differentCase)
	require.False(t, differentCase.Pieces[0].CutSymmetry.Valid,
		"the store treats a differently-cased key as a NEW row; the carry must not give that row somebody else's pairing")
}

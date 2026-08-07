package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// unmarkedConstructionDigest is the CONSTRUCTION fingerprint of the fixture below as computed by the
// code that shipped BEFORE migration 0275 — captured by running this exact fixture against
// constructionProjection at the parent commit, not by re-deriving it from the code under test.
//
// It is the whole safety argument of the conditional tail in one constant. `p.Mirrored` already sits
// in the tuple and digestOf is sha256(json.Marshal(...)), which encodes a []any POSITIONALLY, so a
// ninth element written UNCONDITIONALLY would move this value for every card in the database and
// declare every approved CONSTRUCTION sign-off stale the moment the deploy landed — before a single
// person had marked a single piece. A hash that has to stay equal to a number recorded from the old
// code is the only form of that claim a test can actually check.
const unmarkedConstructionDigest = "cff82c699db5face04eb5d10487a671b8ea11a4de6f45521e56f506db1fb1cb5"

// cutSymmetryDigestFixture is a card with content in every part of the CONSTRUCTION projection —
// header, one operation, three pieces — so the pin covers the tuple's shape and not just an empty
// slice. All three pieces are UNMARKED, which is the state of every row in the database on the day
// 0275 is applied.
func cutSymmetryDigestFixture() *entity.TechCardInsert {
	return &entity.TechCardInsert{
		Construction: &entity.TechCardConstruction{
			MainStitchType:  sql.NullString{String: "lockstitch 301", Valid: true},
			StitchDensity:   sql.NullString{String: "4/cm", Valid: true},
			OverlockThreads: sql.NullString{String: "4-thread", Valid: true},
			SeamAllowances:  sql.NullString{String: "1 cm", Valid: true},
		},
		Operations: []entity.TechCardOperation{
			{
				Node:        "assembly",
				Description: sql.NullString{String: "втачать рукав", Valid: true},
				SeamType:    sql.NullString{String: "overlock", Valid: true},
			},
		},
		Pieces: []entity.TechCardPiece{
			{LineKey: "01DGSTPIECEFRONT0000000F1", Name: "полочка", PiecesPerGarment: 2, Grainline: "lengthwise",
				Note: sql.NullString{String: "left + right", Valid: true}},
			{LineKey: "01DGSTPIECEBACK00000000B1", Name: "спинка", PiecesPerGarment: 1, Grainline: "lengthwise"},
			{LineKey: "01DGSTPIECECUFF00000000C1", Name: "манжета", PiecesPerGarment: 2, Grainline: "crosswise", Fused: true},
		},
	}
}

// An UNMARKED card must hash exactly as it did before 0275. This is the test the design exists for:
// the re-approval wave has to be the size of the marking campaign, not the size of the rollout.
func TestUnmarkedCardConstructionDigestIsUnchangedBy0275(t *testing.T) {
	got := TechCardSectionDigests(cutSymmetryDigestFixture())[entity.SignoffConstruction]
	require.Equal(t, unmarkedConstructionDigest, got,
		"an unmarked card's CONSTRUCTION digest moved: every approved sign-off in the database just went stale at deploy time")
}

// And the converse, or the test above would be satisfied by a field nobody hashes: marking ONE piece
// must move the fingerprint. CONSTRUCTION is the signature under what is cut and how, and "these two
// panels are a mirrored pair" changes the physical part that comes out.
func TestMarkingCutSymmetryMovesTheConstructionDigest(t *testing.T) {
	for _, sym := range []entity.TechCardPieceCutSymmetry{
		entity.PieceCutSymmetryIdentical,
		entity.PieceCutSymmetryMirrored,
		entity.PieceCutSymmetryFold,
	} {
		tc := cutSymmetryDigestFixture()
		tc.Pieces[0].CutSymmetry = sql.NullString{String: string(sym), Valid: true}
		got := TechCardSectionDigests(tc)[entity.SignoffConstruction]
		require.NotEqual(t, unmarkedConstructionDigest, got,
			"marking a piece %q left the CONSTRUCTION digest untouched — the sign-off would cover an "+
				"instruction to the floor that it never saw", sym)
	}

	// Including «identical»: it is an ANSWER, not the absence of one. A card where somebody has
	// confirmed the panels are congruent copies says something different to the floor than a card
	// nobody has looked at, and must not hash the same.
	identical := cutSymmetryDigestFixture()
	identical.Pieces[0].CutSymmetry = sql.NullString{String: string(entity.PieceCutSymmetryIdentical), Valid: true}
	require.NotEqual(t,
		TechCardSectionDigests(cutSymmetryDigestFixture())[entity.SignoffConstruction],
		TechCardSectionDigests(identical)[entity.SignoffConstruction])

	// The three values must not collapse into each other either — a tail that appended a constant
	// would pass everything above.
	seen := map[string]entity.TechCardPieceCutSymmetry{}
	for _, sym := range []entity.TechCardPieceCutSymmetry{
		entity.PieceCutSymmetryIdentical,
		entity.PieceCutSymmetryMirrored,
		entity.PieceCutSymmetryFold,
	} {
		tc := cutSymmetryDigestFixture()
		tc.Pieces[0].CutSymmetry = sql.NullString{String: string(sym), Valid: true}
		d := TechCardSectionDigests(tc)[entity.SignoffConstruction]
		if prev, dup := seen[d]; dup {
			t.Fatalf("cut symmetry %q and %q hash to the same CONSTRUCTION digest", prev, sym)
		}
		seen[d] = sym
	}
}

// A marking on one piece must not be readable as a marking on another: the tail is appended per row,
// so two cards differing only in WHICH piece is marked must differ.
func TestCutSymmetryDigestIsPerPiece(t *testing.T) {
	first := cutSymmetryDigestFixture()
	first.Pieces[0].CutSymmetry = sql.NullString{String: string(entity.PieceCutSymmetryMirrored), Valid: true}
	third := cutSymmetryDigestFixture()
	third.Pieces[2].CutSymmetry = sql.NullString{String: string(entity.PieceCutSymmetryMirrored), Valid: true}
	require.NotEqual(t,
		TechCardSectionDigests(first)[entity.SignoffConstruction],
		TechCardSectionDigests(third)[entity.SignoffConstruction])
}

// The retired `mirrored` column stays in slot 4 forever, frozen false. Removing it breaks the
// fingerprint exactly the way an unconditional append would — dropping an element from a positional
// JSON array shifts every element after it — so the digest pinned above is also the guard that stops
// a future cleanup from doing it. This test states the intent in words next to the constant.
func TestRetiredMirroredStaysInTheConstructionTuple(t *testing.T) {
	tc := cutSymmetryDigestFixture()
	base := TechCardSectionDigests(tc)[entity.SignoffConstruction]
	tc.Pieces[0].Mirrored = true
	require.NotEqual(t, base, TechCardSectionDigests(tc)[entity.SignoffConstruction],
		"slot 4 must still be occupied by p.Mirrored; if this passes as equal the element was removed "+
			"and every stored CONSTRUCTION digest in the database is now unreachable")
}

package design_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// M6 (07.10, flat-consistency 107): THE PIECES LIST OF PARTS. A read of the FRONT/BACK plates is the
// list only while no designer edited it; after an edit it is a PROPOSAL and the designer's names stay
// exactly as written (a forced re-read once wiped 8 designer edits of the join list).
//
// Run like every probe of this package: a disposable MySQL container and CI=1 (wave2_db_test.go).
//
// MUTATIONS THEY CATCH: a read written over an edited list; a proposal that bumps rev (re-labels for
// nothing) or changes the names; a read applied over a row that moved since the reader looked; a
// designer save without CAS; settle not taking the proposal's plates (the next visit reads again);
// the band without the list.

func piecesDoc(names ...string) entity.DesignPartsPiecesDoc {
	d := entity.DesignPartsPiecesDoc{}
	for _, n := range names {
		d.Pieces = append(d.Pieces, entity.DesignPartsPiece{Name: n, Views: []string{"front"}})
	}
	return d
}

func TestDesignDBPartsPiecesReadNeverOverwritesTheDesigner(t *testing.T) {
	rep, raw := probeRepository(t)
	card := probeCard(t, raw)
	ctx := context.Background()
	d := rep.Design()

	got, err := d.GetPartsPieces(ctx, card)
	require.NoError(t, err)
	require.Nil(t, got, "no list yet")

	// the first read is the list
	got, err = d.SavePartsPiecesRead(ctx, entity.DesignPartsPiecesRead{TechCardId: card, ExpectedRev: 0,
		Doc: piecesDoc("front body", "inner front v"), Front: 11, Back: 12, Model: "m1"})
	require.NoError(t, err)
	require.Equal(t, 1, got.Rev)
	require.Equal(t, []string{"front body", "inner front v"}, got.Doc.Names())
	require.False(t, got.Edited())
	require.Equal(t, [2]int{11, 12}, [2]int{got.Front, got.Back})

	// a second reader that saw no row is late: the row stands
	late, err := d.SavePartsPiecesRead(ctx, entity.DesignPartsPiecesRead{TechCardId: card, ExpectedRev: 0,
		Doc: piecesDoc("x panel"), Front: 11, Back: 12, Model: "m9"})
	require.NoError(t, err)
	require.Equal(t, 1, late.Rev)
	require.Equal(t, []string{"front body", "inner front v"}, late.Doc.Names())

	// new plates, no edit: the read replaces the list
	got, err = d.SavePartsPiecesRead(ctx, entity.DesignPartsPiecesRead{TechCardId: card, ExpectedRev: 1,
		Doc: piecesDoc("front body", "neckband"), Front: 21, Back: 22, Model: "m2"})
	require.NoError(t, err)
	require.Equal(t, 2, got.Rev)
	require.Equal(t, []string{"front body", "neckband"}, got.Doc.Names())

	// the designer saves under CAS; a stale tab is refused
	_, err = d.SetPartsPieces(ctx, entity.DesignPartsPiecesSave{TechCardId: card, ExpectedRev: 1,
		Names: []string{"a"}, Actor: "probe"})
	require.True(t, errors.Is(err, entity.ErrDesignPartsPiecesRevMismatch), "%v", err)
	got, err = d.SetPartsPieces(ctx, entity.DesignPartsPiecesSave{TechCardId: card, ExpectedRev: 2,
		Names: []string{"front body", "neck band", "left strap"}, Actor: "probe"})
	require.NoError(t, err)
	require.Equal(t, 3, got.Rev)
	require.True(t, got.Edited())
	require.Equal(t, "probe", got.EditedBy)
	require.Equal(t, []string{"front"}, got.Doc.Pieces[0].Views, "a kept name keeps the views the read saw")
	require.Empty(t, got.Doc.Pieces[1].Views, "a designer's own piece has none")

	// new plates after the edit: the read only PROPOSES — the names stay, the rev moves
	got, err = d.SavePartsPiecesRead(ctx, entity.DesignPartsPiecesRead{TechCardId: card, ExpectedRev: 3,
		Doc: piecesDoc("front body", "armhole thing"), Front: 31, Back: 32, Model: "m3"})
	require.NoError(t, err)
	require.Equal(t, 4, got.Rev, "a proposal moves the rev (a settle is tied to it) but no name")
	require.Equal(t, []string{"front body", "neck band", "left strap"}, got.Doc.Names())
	require.NotNil(t, got.Proposal)
	require.Equal(t, []string{"front body", "armhole thing"}, got.Proposal.Doc.Names())
	require.Equal(t, [2]int{31, 32}, [2]int{got.Proposal.Front, got.Proposal.Back})
	f, b := got.ReadFrom()
	require.Equal(t, [2]int{31, 32}, [2]int{f, b}, "the newest read is the proposal's")
	require.Equal(t, [2]int{21, 22}, [2]int{got.Front, got.Back}, "the list's own plates stay")

	// a reader that saw an older rev changes nothing
	got, err = d.SavePartsPiecesRead(ctx, entity.DesignPartsPiecesRead{TechCardId: card, ExpectedRev: 2,
		Doc: piecesDoc("zzz panel"), Front: 41, Back: 42, Model: "m4"})
	require.NoError(t, err)
	require.Equal(t, []string{"front body", "armhole thing"}, got.Proposal.Doc.Names())

	// a tab that saw the list before the proposal cannot settle (or edit) blind
	_, err = d.SetPartsPieces(ctx, entity.DesignPartsPiecesSave{TechCardId: card, ExpectedRev: 3,
		Names: []string{"front body"}, SettleProposal: true, Actor: "probe"})
	require.True(t, errors.Is(err, entity.ErrDesignPartsPiecesRevMismatch), "%v", err)

	// an edit without settling keeps the proposal waiting
	got, err = d.SetPartsPieces(ctx, entity.DesignPartsPiecesSave{TechCardId: card, ExpectedRev: 4,
		Names: []string{"front body", "neck band"}, Actor: "probe"})
	require.NoError(t, err)
	require.Equal(t, 5, got.Rev)
	require.NotNil(t, got.Proposal)

	// settle (take): the proposal goes, its plates become the list's
	got, err = d.SetPartsPieces(ctx, entity.DesignPartsPiecesSave{TechCardId: card, ExpectedRev: 5,
		Names: got.Proposal.Doc.Names(), SettleProposal: true, Actor: "probe"})
	require.NoError(t, err)
	require.Equal(t, 6, got.Rev)
	require.Nil(t, got.Proposal)
	require.Equal(t, []string{"front body", "armhole thing"}, got.Doc.Names())
	require.Equal(t, [2]int{31, 32}, [2]int{got.Front, got.Back})
	require.Equal(t, "m3", got.Model)
	require.Equal(t, []string{"front"}, got.Doc.Pieces[1].Views, "a taken name keeps the proposal's views")
	require.True(t, got.Edited(), "still the designer's list: the next read proposes again")

	// the band carries the list and the flat plates it is checked against
	band, err := d.GetBand(ctx, card, 10)
	require.NoError(t, err)
	require.NotNil(t, band.PartsPieces)
	require.Equal(t, 6, band.PartsPieces.Rev)
	require.NotNil(t, band.FlatMedia)

	// the row goes with the card
	_, err = raw.Exec(`DELETE FROM tech_card WHERE id = ?`, card)
	require.NoError(t, err)
	var n int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM design_parts_pieces WHERE tech_card_id = ?`, card).Scan(&n))
	require.Zero(t, n)
}

func TestDesignDBPartsPiecesFirstSaveIsTheDesigners(t *testing.T) {
	rep, raw := probeRepository(t)
	card := probeCard(t, raw)
	ctx := context.Background()
	d := rep.Design()

	_, err := d.SetPartsPieces(ctx, entity.DesignPartsPiecesSave{TechCardId: card, ExpectedRev: 1,
		Names: []string{"a"}, Actor: "probe"})
	require.True(t, errors.Is(err, entity.ErrDesignPartsPiecesRevMismatch), "no row is rev 0: %v", err)
	got, err := d.SetPartsPieces(ctx, entity.DesignPartsPiecesSave{TechCardId: card, ExpectedRev: 0,
		Names: []string{"front body"}, Actor: "probe"})
	require.NoError(t, err)
	require.Equal(t, 1, got.Rev)
	require.True(t, got.Edited())
	// a read of any plates now only proposes
	got, err = d.SavePartsPiecesRead(ctx, entity.DesignPartsPiecesRead{TechCardId: card, ExpectedRev: 1,
		Doc: piecesDoc("back body"), Front: 5, Model: "m"})
	require.NoError(t, err)
	require.Equal(t, []string{"front body"}, got.Doc.Names())
	require.NotNil(t, got.Proposal)
}

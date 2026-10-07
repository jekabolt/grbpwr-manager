package entity

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ═══ PARTS · THE PIECES LIST (M6, flat-consistency 107) ═══
//
// The closed list of part names the PARTS labeller may use. Read by a model from the card's ACCEPTED
// FRONT/BACK flats (the plates of the FLAT bench slots — never the photos, never the join list),
// edited by the designer, never sent to image generation. One row per card (design_parts_pieces).
//
// A READ NEVER WRITES OVER A DESIGNER'S EDITS: once the list was saved by a designer (EditedAt set),
// a read of changed flats is held in Proposal until the designer takes or keeps it.

const (
	// DesignPartsPiecesMax — pieces in one list (a read's answer is cut here, a designer's save refused).
	DesignPartsPiecesMax = 30
	// DesignPartsPieceMaxRunes — one name; the labeller's label ceiling (DesignPartsMaxLabelRunes).
	DesignPartsPieceMaxRunes = DesignPartsMaxLabelRunes
	// DesignPartsOpeningsMax / DesignPartsOpeningMaxRunes — the read's opening words.
	DesignPartsOpeningsMax     = 6
	DesignPartsOpeningMaxRunes = 40
)

// DesignPartsPiece — one cut piece: its name and the views the read saw it on (front | back; empty =
// a designer's piece).
type DesignPartsPiece struct {
	Name  string   `json:"name"`
	Views []string `json:"views,omitempty"`
}

// DesignPartsPiecesDoc — the list as stored (the `pieces` column, and the body of a proposal).
type DesignPartsPiecesDoc struct {
	Pieces   []DesignPartsPiece `json:"pieces"`
	Openings []string           `json:"openings,omitempty"`
}

// Names — the pieces' names in order.
func (d DesignPartsPiecesDoc) Names() []string {
	out := make([]string, 0, len(d.Pieces))
	for _, p := range d.Pieces {
		out = append(out, p.Name)
	}
	return out
}

// DesignPartsPiecesProposal — a newer read held for the designer (the `proposal` column).
type DesignPartsPiecesProposal struct {
	Doc    DesignPartsPiecesDoc `json:"doc"`
	Model  string               `json:"model"`
	Front  int                  `json:"front_media_id"`
	Back   int                  `json:"back_media_id"`
	ReadAt time.Time            `json:"read_at"`
}

// DesignPartsPieces — the card's row.
type DesignPartsPieces struct {
	TechCardId int
	Rev        int
	Doc        DesignPartsPiecesDoc
	// Front / Back — the bench plates the list (as read) came from; 0 = that side had none.
	Front, Back int
	Proposal    *DesignPartsPiecesProposal
	Model       string
	EditedBy    string
	EditedAt    sql.NullTime
	UpdatedAt   time.Time
}

// Edited — a designer saved the list: a read may only propose.
func (p *DesignPartsPieces) Edited() bool { return p != nil && p.EditedAt.Valid }

// ReadFrom — the FRONT/BACK plates the newest read (the proposal's, else the list's) came from: a
// card whose bench holds other plates needs a read again.
func (p *DesignPartsPieces) ReadFrom() (front, back int) {
	if p.Proposal != nil {
		return p.Proposal.Front, p.Proposal.Back
	}
	return p.Front, p.Back
}

// DesignPartsPiecesRead — the model's read of one FRONT/BACK pair, written by SavePartsPiecesRead.
// ExpectedRev is the rev the reader saw (0 = no row): a row that moved meanwhile is left alone.
type DesignPartsPiecesRead struct {
	TechCardId  int
	ExpectedRev int
	Doc         DesignPartsPiecesDoc
	Front, Back int
	Model       string
}

// DesignPartsPiecesSave — a designer's save of the whole list under CAS (ExpectedRev 0 = no row yet).
// SettleProposal drops a pending proposal (the names are the designer's answer to it).
type DesignPartsPiecesSave struct {
	TechCardId     int
	ExpectedRev    int
	Names          []string
	SettleProposal bool
	Actor          string
}

// ErrDesignPartsPiecesRevMismatch — the CAS of SetDesignPartsPieces.
var ErrDesignPartsPiecesRevMismatch = errors.New("design: parts_pieces_rev_mismatch")

// DesignPartsPieceNameOf — a name as stored: lowercase, every run of anything but letters and digits
// one space, «-» kept inside a word, trimmed.
func DesignPartsPieceNameOf(raw string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(raw) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		case r == '-' && b.Len() > 0 && !space:
			b.WriteRune(r)
		default:
			space = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// DesignPartsPieceReserved — names the labeller keeps for itself: an opening is no piece, unnamed is
// what nobody named.
func DesignPartsPieceReserved(name string) bool {
	return name == "opening" || name == "openings" || strings.HasPrefix(name, "opening ") ||
		name == DesignPartsUnnamed
}

// CleanDesignPartsPieceNames — a designer's list as it will be stored: names cleaned
// (DesignPartsPieceNameOf), empty and repeated ones dropped. Refused: a reserved name, a name over
// DesignPartsPieceMaxRunes, more than DesignPartsPiecesMax names, or none left.
func CleanDesignPartsPieceNames(names []string) ([]string, error) {
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, raw := range names {
		n := DesignPartsPieceNameOf(raw)
		switch {
		case n == "" || seen[n]:
			continue
		case DesignPartsPieceReserved(n):
			return nil, fmt.Errorf("%q is not a piece: openings and unnamed regions are the labeller's own", n)
		case utf8.RuneCountInString(n) > DesignPartsPieceMaxRunes:
			return nil, fmt.Errorf("%q is longer than %d characters", n, DesignPartsPieceMaxRunes)
		}
		seen[n] = true
		out = append(out, n)
	}
	switch {
	case len(out) == 0:
		return nil, errors.New("the list needs at least one piece")
	case len(out) > DesignPartsPiecesMax:
		return nil, fmt.Errorf("the list holds %d pieces; the ceiling is %d", len(out), DesignPartsPiecesMax)
	}
	return out, nil
}

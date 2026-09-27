package entity

import (
	"database/sql"
	"time"
)

// ColorwayDevelopmentPatch is the writable PLM/lab-dip block of a colourway (product.dev_* /
// product.lab_dip_*). It is a PATCH: a nil field is "leave as it is", so the caller can persist a
// lab-dip decision without also re-sending the colourway's dev code, pantone and swatch — none of
// which any read path returns, so it could not re-send them faithfully even if it wanted to.
//
// Presence comes from UpdateColorwayRequest.update_mask; with no mask, every writable field the
// request carries is applied. Lab-dip audit dates/author are deliberately absent: the store owns
// them and stamps fresh transitions from Actor, which the authenticated handler supplies.
type ColorwayDevelopmentPatch struct {
	DevCode            *string
	Name               *string
	LabDipStatus       *TechCardLabDipStatus
	Comment            *string
	Pantone            *string
	PantoneSystem      *string
	DevHex             *string
	SwatchMediaId      *int
	LabDipRound        *int
	LabDipRejectReason *string
	DisplayOrder       *int
	// Colours is the ordered palette (T45, product_colour): nil = leave the stored palette as it is;
	// non-nil = replace it whole, by position (1…8 colours, already canonicalised by
	// NormalizeColorwayPalette). Colours[0] is the main colour, and writing the palette mirrors it
	// into Pantone / PantoneSystem / DevHex (the store does it, after the scalar merge, so the
	// mirror wins over scalars sent in the same request). A palette edit never touches the
	// per-slot recipe: that is ApplyColorwayPaletteToSlots, an explicit separate write.
	Colours []ColorwayColour
	// NameI18n writes per-language translations of the colourway name (product_colour_name_i18n),
	// keyed by language id: a non-empty value upserts that language, "" deletes it (the storefront
	// then falls back to the operator's name, Name above). nil/empty = leave them as they are.
	NameI18n map[int]string
	Actor    string // server-only authenticated username; never parsed from the wire
}

// IsEmpty reports whether the patch would change nothing, so the store can skip the work entirely.
func (p *ColorwayDevelopmentPatch) IsEmpty() bool {
	return p == nil || (!p.HasScalars() && p.Colours == nil && len(p.NameI18n) == 0)
}

// HasScalars reports whether the patch changes any product.dev_* / lab_dip_* / pantone column —
// the part applied as one merged UPDATE of the product row.
func (p *ColorwayDevelopmentPatch) HasScalars() bool {
	return p != nil && (p.DevCode != nil || p.Name != nil || p.LabDipStatus != nil || p.Comment != nil ||
		p.Pantone != nil || p.PantoneSystem != nil || p.DevHex != nil || p.SwatchMediaId != nil ||
		p.LabDipRound != nil || p.LabDipRejectReason != nil || p.DisplayOrder != nil)
}

// MainColour is the palette's first colour, or nil when the patch writes no palette.
func (p *ColorwayDevelopmentPatch) MainColour() *ColorwayColour {
	if p == nil || len(p.Colours) == 0 {
		return nil
	}
	return &p.Colours[0]
}

// TouchesLabDip reports whether the patch changes any lab-dip field — i.e. whether the round journal
// (product_lab_dip_round) has anything to record. A patch that only renames the colourway does not
// open a round.
func (p *ColorwayDevelopmentPatch) TouchesLabDip() bool {
	return p != nil && (p.LabDipStatus != nil || p.LabDipRound != nil || p.LabDipRejectReason != nil)
}

// ColorwayLabDipRound is one recorded round of a colourway's lab-dip loop (product_lab_dip_round).
// Read-only on the wire: the journal is written from the lab-dip write path, keyed by round number,
// so an earlier round is never overwritten by a later one.
type ColorwayLabDipRound struct {
	ProductId     int                  `db:"product_id"`
	RoundNumber   int                  `db:"round_number"`
	Status        TechCardLabDipStatus `db:"status"`
	SubmittedAt   sql.NullTime         `db:"submitted_at"`
	DecidedAt     sql.NullTime         `db:"decided_at"`
	DecidedBy     sql.NullString       `db:"decided_by"`
	RejectReason  sql.NullString       `db:"reject_reason"`
	Comment       sql.NullString       `db:"comment"`
	SwatchMediaId sql.NullInt32        `db:"swatch_media_id"`
	CreatedAt     time.Time            `db:"created_at"`
}

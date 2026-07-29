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
// Presence comes from UpdateColorwayRequest.update_mask; with no mask, every field the request
// carries is applied. Dates are sql.NullTime so "clear the decision date" is expressible and
// distinguishable from "do not touch it" (a nil pointer).
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
	LabDipSubmittedAt  *sql.NullTime
	LabDipDecidedAt    *sql.NullTime
	LabDipDecidedBy    *string
	LabDipRejectReason *string
	DisplayOrder       *int
}

// IsEmpty reports whether the patch would change nothing, so the store can skip the work entirely.
func (p *ColorwayDevelopmentPatch) IsEmpty() bool {
	return p == nil || (p.DevCode == nil && p.Name == nil && p.LabDipStatus == nil && p.Comment == nil &&
		p.Pantone == nil && p.PantoneSystem == nil && p.DevHex == nil && p.SwatchMediaId == nil &&
		p.LabDipRound == nil && p.LabDipSubmittedAt == nil && p.LabDipDecidedAt == nil &&
		p.LabDipDecidedBy == nil && p.LabDipRejectReason == nil && p.DisplayOrder == nil)
}

// TouchesLabDip reports whether the patch changes any lab-dip field — i.e. whether the round journal
// (product_lab_dip_round) has anything to record. A patch that only renames the colourway does not
// open a round.
func (p *ColorwayDevelopmentPatch) TouchesLabDip() bool {
	return p != nil && (p.LabDipStatus != nil || p.LabDipRound != nil || p.LabDipSubmittedAt != nil ||
		p.LabDipDecidedAt != nil || p.LabDipDecidedBy != nil || p.LabDipRejectReason != nil)
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

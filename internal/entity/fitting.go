package entity

import (
	"database/sql"
	"time"

	"github.com/shopspring/decimal"
)

// FittingStatus is the lifecycle state of a fitting session.
type FittingStatus string

const (
	FittingPlanned   FittingStatus = "planned"
	FittingDone      FittingStatus = "done"
	FittingCancelled FittingStatus = "cancelled"
)

// ValidFittingStatuses is the set of accepted fitting statuses.
var ValidFittingStatuses = map[FittingStatus]bool{
	FittingPlanned:   true,
	FittingDone:      true,
	FittingCancelled: true,
}

// FittingVerdict is the outcome of a fitting session.
type FittingVerdict string

const (
	FittingPending     FittingVerdict = "pending"
	FittingApproved    FittingVerdict = "approved"
	FittingNeedsRework FittingVerdict = "needs_rework"
	FittingRejected    FittingVerdict = "rejected"
)

// ValidFittingVerdicts is the set of accepted fitting verdicts.
var ValidFittingVerdicts = map[FittingVerdict]bool{
	FittingPending:     true,
	FittingApproved:    true,
	FittingNeedsRework: true,
	FittingRejected:    true,
}

// FittingSize is one size tried in a fitting, with an optional per-size fit note.
type FittingSize struct {
	SizeId  int            `db:"size_id"`
	FitNote sql.NullString `db:"fit_note"`
}

// FittingPattern is a PDF cut-pattern (выкройка) iteration measured in a fitting. It is a
// snapshot of the uploaded file (url + filename), not a live reference to a tech-card
// pattern — the tech card holds the final pattern, a fitting captures the iteration tried.
type FittingPattern struct {
	SizeId    sql.NullInt32  `db:"size_id"`
	URL       string         `db:"url"`
	Filename  sql.NullString `db:"filename"`
	SizeBytes sql.NullInt64  `db:"size_bytes"`
}

// FittingCallout is a numbered marker pinned to a fitting photo, flagging a fit
// problem at a point on the image (a pin + a note). Simpler than TechCardCallout —
// no part/dimensions (a fitting flags posadka, not spec geometry).
type FittingCallout struct {
	Number  int                 `db:"callout_number"`
	Note    sql.NullString      `db:"note"`
	MediaId sql.NullInt32       `db:"media_id"` // the fitting photo this callout is pinned to
	PosX    decimal.NullDecimal `db:"pos_x"`    // normalised 0..1 marker position
	PosY    decimal.NullDecimal `db:"pos_y"`
}

// FittingOutcome is the structured result of a fitting round (distinct from the free Verdict):
// what the team decided to DO next. Approved = the round passed; NewRound = another try-on is
// needed; Dropped = the style/sample was abandoned. NULL = not yet decided.
type FittingOutcome string

const (
	FittingOutcomeApproved FittingOutcome = "approved"
	FittingOutcomeNewRound FittingOutcome = "new_round"
	FittingOutcomeDropped  FittingOutcome = "dropped"
)

// ValidFittingOutcomes is the accepted outcome set.
var ValidFittingOutcomes = map[FittingOutcome]bool{
	FittingOutcomeApproved: true,
	FittingOutcomeNewRound: true,
	FittingOutcomeDropped:  true,
}

// ValidFittingChangeTargets is the accepted target set for a change request.
var ValidFittingChangeTargets = map[string]bool{
	"pattern": true, "construction": true, "material": true, "grading": true, "other": true,
}

// FittingChangeRequest is one structured "what to change" item produced by a fitting (task 13):
// the target area (pattern / construction / material / grading / other), a free note, an optional
// link to a photo callout pin, and a resolved flag (set when carried into the tech card). It is a
// full-replace child of the fitting, like callouts.
type FittingChangeRequest struct {
	Id            int            `db:"id"`
	Target        string         `db:"target"`
	Note          string         `db:"note"`
	CalloutNumber sql.NullInt32  `db:"callout_number"`
	Resolved      bool           `db:"resolved"`
}

// FittingInsert is the writable payload for a fitting session. A fitting anchors
// to a tech card (the style) and/or a specific product (the colour/SKU sample);
// at least one of TechCardId / ProductId is set (enforced in the API layer).
type FittingInsert struct {
	TechCardId     sql.NullInt32          `db:"tech_card_id"`
	ProductId      sql.NullInt32          `db:"product_id"`
	ModelId        sql.NullInt32          `db:"model_id"`
	FittingDate    time.Time              `db:"fitting_date"`
	Comment        sql.NullString         `db:"comment"`
	Status         FittingStatus          `db:"status"`
	Verdict        FittingVerdict         `db:"verdict"`
	RecordedBy     sql.NullString         `db:"recorded_by"`
	RoundNumber    sql.NullInt32          `db:"round_number"` // # in the card's try-on sequence; auto-assigned when unset
	Outcome        sql.NullString         `db:"outcome"`      // FittingOutcome; NULL = undecided
	SampleId       sql.NullInt32          `db:"sample_id"`    // the sample this fitting tried on (NF-04)
	Sizes          []FittingSize          `db:"-"`
	MediaIds       []int                  `db:"-"`
	Patterns       []FittingPattern       `db:"-"`
	Callouts       []FittingCallout       `db:"-"`
	ChangeRequests []FittingChangeRequest `db:"-"`
}

// Fitting is a stored fitting session (fitting row + sizes + resolved media).
type Fitting struct {
	Id int `db:"id"`
	FittingInsert
	Media     []MediaFull `db:"-"`
	CreatedAt time.Time   `db:"created_at"`
	UpdatedAt time.Time   `db:"updated_at"`
}

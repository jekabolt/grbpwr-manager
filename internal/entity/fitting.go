package entity

import (
	"database/sql"
	"time"
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

// FittingInsert is the writable payload for a fitting session.
type FittingInsert struct {
	ProductId   int            `db:"product_id"`
	ModelId     sql.NullInt32  `db:"model_id"`
	FittingDate time.Time      `db:"fitting_date"`
	Comment     sql.NullString `db:"comment"`
	Status      FittingStatus  `db:"status"`
	Verdict     FittingVerdict `db:"verdict"`
	RecordedBy  sql.NullString `db:"recorded_by"`
	Sizes       []FittingSize  `db:"-"`
	MediaIds    []int          `db:"-"`
}

// Fitting is a stored fitting session (fitting row + sizes + resolved media).
type Fitting struct {
	Id int `db:"id"`
	FittingInsert
	Media     []MediaFull `db:"-"`
	CreatedAt time.Time   `db:"created_at"`
	UpdatedAt time.Time   `db:"updated_at"`
}

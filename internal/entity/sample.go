package entity

import (
	"database/sql"
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// ErrSampleHasMovements is returned when deleting a sample that has material stock movements — its
// issues are applied facts and dropping the sample would orphan them.
var ErrSampleHasMovements = errors.New("sample has material movements")

// ErrSampleColorwayForeign is returned when a sample's colorway_id does not belong to the sample's
// tech card (a colour-model from another style).
var ErrSampleColorwayForeign = errors.New("colorway does not belong to the sample's tech card")

// ErrSampleSizeForeign is returned when a sample's size_id is not part of the sample's tech-card
// size grid.
var ErrSampleSizeForeign = errors.New("size is not in the sample's tech card size grid")

// ErrSampleForeignToCard is returned when a sample linked from another artifact (a fitting or a
// dev-expense) belongs to a different tech card than that artifact — linking it would attribute one
// style's work/cost to another.
var ErrSampleForeignToCard = errors.New("sample belongs to a different tech card")

// ErrFittingForeignToCard is returned when a dev-expense's fitting_id points at a fitting anchored on
// a different tech card (directly, or via its product's style) — attributing it would land one style's
// R&D spend on another style's round (the S20/Q8 attribution the frontend had dead-coded to 0).
var ErrFittingForeignToCard = errors.New("fitting belongs to a different tech card")

// Sample purpose (mirrors the tech-card stages that produce a physical sample).
const (
	SamplePurposeProto = "proto" // first prototype
	SamplePurposeFit   = "fit"   // fit sample
	SamplePurposeSMS   = "sms"   // salesman sample
	SamplePurposePP    = "pp"    // pre-production / colour model
)

// ValidSamplePurposes is the closed set of sample purposes (DB CHECK + dto validation).
var ValidSamplePurposes = map[string]bool{
	SamplePurposeProto: true, SamplePurposeFit: true, SamplePurposeSMS: true, SamplePurposePP: true,
}

// Sample status.
const (
	SampleStatusPlanned  = "planned"
	SampleStatusInSewing = "in_sewing"
	SampleStatusDone     = "done"
	SampleStatusScrapped = "scrapped"
)

// ValidSampleStatuses is the closed set of sample statuses.
var ValidSampleStatuses = map[string]bool{
	SampleStatusPlanned: true, SampleStatusInSewing: true, SampleStatusDone: true, SampleStatusScrapped: true,
}

// Sample fabric source.
const (
	SampleFabricSample     = "sample"     // sample fabric
	SampleFabricProduction = "production" // production fabric (pp / colour model)
)

// ValidSampleFabricSources is the closed set of fabric sources.
var ValidSampleFabricSources = map[string]bool{
	SampleFabricSample: true, SampleFabricProduction: true,
}

// SampleInsert is the writable payload of a sample. Number is server-assigned (MAX+1 per card).
type SampleInsert struct {
	TechCardId   int            `db:"tech_card_id"`
	Purpose      string         `db:"purpose"`
	SizeId       sql.NullInt32  `db:"size_id"`
	ColorwayId   sql.NullInt32  `db:"colorway_id"`
	Status       string         `db:"status"`
	FabricSource string         `db:"fabric_source"`
	Notes        sql.NullString `db:"notes"`
	StartedAt    sql.NullTime   `db:"started_at"`
	FinishedAt   sql.NullTime   `db:"finished_at"`
	PatternUrl   sql.NullString `db:"pattern_url"`  // snapshot of the pattern iteration cut (B-3/gap-03)
	PatternNote  sql.NullString `db:"pattern_note"` // free-text pattern reference
	// MediaIds is the write-side list of sample-photo media (B-6); full-replace on update. Not a
	// column — persisted to sample_media.
	MediaIds []int `db:"-"`
}

// Sample is a stored sample: the writable payload plus identity, its per-card number and timestamps.
// Cost is composed on read (GetSampleById) and is nil on list.
type Sample struct {
	Id int `db:"id"`
	SampleInsert
	Number    int         `db:"number"`
	CreatedAt time.Time   `db:"created_at"`
	UpdatedAt time.Time   `db:"updated_at"`
	Cost      *SampleCost `db:"-"`
	Media     []MediaFull `db:"-"` // resolved sample photos (B-6), populated by Get/List
}

// SampleCost is the composed cost of a sample (base currency): materials issued from the warehouse
// (NF-01) plus the manual dev-expense journal rows tied to this sample. HasUncosted is true when a
// material issue had no known average (its base value is missing from the materials total).
type SampleCost struct {
	MaterialsBase decimal.Decimal
	ManualBase    decimal.Decimal
	TotalBase     decimal.Decimal
	HasUncosted   bool
}

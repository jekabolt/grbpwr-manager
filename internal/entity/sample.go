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

// ErrSampleConflict is returned by UpdateSample when the caller's expected lock_version does not
// match the stored one (S25) — a concurrent edit landed between the read and the save. The caller
// should reload and retry (mirrors ErrTechCardConflict / ErrMaterialConflict). ABORTED upstream.
var ErrSampleConflict = errors.New("sample was modified concurrently")

// ErrSampleSpecReleaseForeign is returned when a sample's spec_release_id references a release that
// belongs to a different tech card — the spec snapshot must be one of this style's own releases.
var ErrSampleSpecReleaseForeign = errors.New("spec release does not belong to the sample's tech card")

// ErrSamplePreviousForeign is returned when a sample's previous_sample_id references a sample of a
// different tech card — the iteration chain must stay within one style.
var ErrSamplePreviousForeign = errors.New("previous sample belongs to a different tech card")

// ErrSampleSubstitutionBomForeign is returned when a substitution's bom_item_id references a BOM line
// that does not belong to the sample's tech card — a line from another style would be a silent mislink.
var ErrSampleSubstitutionBomForeign = errors.New("bom line does not belong to the sample's tech card")

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
	// Round spine (Q7/§2.7). RoundNumber/PreviousSampleId are auto-assigned by the store when unset.
	RoundNumber      sql.NullInt32 `db:"round_number"`       // the style's iteration index
	SpecReleaseId    sql.NullInt32 `db:"spec_release_id"`    // FK tech_card_release: the immutable spec snapshot sewn from
	PreviousSampleId sql.NullInt32 `db:"previous_sample_id"` // FK sample: the prior round's sample (chain)
	// Audit stamps (§2.11): server-set from the JWT, no FK. CreatedBy is written once on create,
	// UpdatedBy on every write.
	CreatedBy string `db:"created_by"`
	UpdatedBy string `db:"updated_by"`
	// MediaIds is the write-side list of sample-photo media (B-6); full-replace on update. Not a
	// column — persisted to sample_media.
	MediaIds []int `db:"-"`
}

// Sample is a stored sample: the writable payload plus identity, its per-card number and timestamps.
// Cost is composed on read (GetSampleById) and is nil on list.
type Sample struct {
	Id int `db:"id"`
	SampleInsert
	Number      int         `db:"number"`
	LockVersion int         `db:"lock_version"` // optimistic-lock counter (S25); echoed on UpdateSample
	CreatedAt   time.Time   `db:"created_at"`
	UpdatedAt   time.Time   `db:"updated_at"`
	Cost        *SampleCost `db:"-"`
	Media       []MediaFull `db:"-"` // resolved sample photos (B-6), populated by Get/List
}

// SampleSubstitutionInsert is the writable payload of a dev-time material substitution on a sample
// (§2.7). Q2 invariant: documentation only, never COGS. CreatedBy is server-stamped.
type SampleSubstitutionInsert struct {
	SampleId              int                 `db:"sample_id"`
	BomItemId             sql.NullInt32       `db:"bom_item_id"`
	OriginalMaterialId    sql.NullInt32       `db:"original_material_id"`
	SubstitutedMaterialId sql.NullInt32       `db:"substituted_material_id"`
	Reason                sql.NullString      `db:"reason"`
	PlannedQty            decimal.NullDecimal `db:"planned_qty"`
	ActualQty             decimal.NullDecimal `db:"actual_qty"`
	CreatedBy             string              `db:"created_by"`
}

// SampleSubstitution is a stored substitution row.
type SampleSubstitution struct {
	Id int `db:"id"`
	SampleSubstitutionInsert
	CreatedAt time.Time `db:"created_at"`
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

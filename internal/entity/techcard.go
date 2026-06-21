package entity

import (
	"database/sql"
	"time"

	"github.com/shopspring/decimal"
)

// TechCardStage is the development stage of a tech card. It mirrors the
// common.TechCardStage proto enum and is stored as a string in tech_card.stage.
type TechCardStage string

const (
	TechCardStageProto TechCardStage = "proto" // prototype
	TechCardStageFit   TechCardStage = "fit"   // fit sample
	TechCardStageSMS   TechCardStage = "sms"   // salesman sample
	TechCardStagePP    TechCardStage = "pp"    // pre-production
	TechCardStageProd  TechCardStage = "prod"  // production
)

// ValidTechCardStages is the set of accepted tech-card stages.
var ValidTechCardStages = map[TechCardStage]bool{
	TechCardStageProto: true,
	TechCardStageFit:   true,
	TechCardStageSMS:   true,
	TechCardStagePP:    true,
	TechCardStageProd:  true,
}

// IsValidTechCardStage reports whether s is an accepted stage.
func IsValidTechCardStage(s TechCardStage) bool {
	return ValidTechCardStages[s]
}

// TechCardMediaKind classifies a tech-card sketch image. It mirrors the
// common.TechCardMediaKind proto enum and is stored as a string in
// tech_card_media.kind.
type TechCardMediaKind string

const (
	TechCardMediaFront   TechCardMediaKind = "front"
	TechCardMediaBack    TechCardMediaKind = "back"
	TechCardMediaDetail  TechCardMediaKind = "detail"
	TechCardMediaLining  TechCardMediaKind = "lining"
	TechCardMediaPreview TechCardMediaKind = "preview"
)

// ValidTechCardMediaKinds is the set of accepted sketch-media kinds.
var ValidTechCardMediaKinds = map[TechCardMediaKind]bool{
	TechCardMediaFront:   true,
	TechCardMediaBack:    true,
	TechCardMediaDetail:  true,
	TechCardMediaLining:  true,
	TechCardMediaPreview: true,
}

// IsValidTechCardMediaKind reports whether k is an accepted media kind.
func IsValidTechCardMediaKind(k TechCardMediaKind) bool {
	return ValidTechCardMediaKinds[k]
}

// TechCardMediaItem is a writable sketch-media reference (id + kind).
type TechCardMediaItem struct {
	MediaId int               `db:"media_id"`
	Kind    TechCardMediaKind `db:"kind"`
}

// TechCardMediaFull is a resolved sketch-media reference for display.
type TechCardMediaFull struct {
	Media MediaFull
	Kind  TechCardMediaKind
}

// TechCardCallout is a numbered detail note pointing at the technical sketch.
type TechCardCallout struct {
	Number      int            `db:"callout_number"`
	Part        sql.NullString `db:"part"`
	Description sql.NullString `db:"description"`
	Dimensions  sql.NullString `db:"dimensions"`
}

// TechCardRevision is one entry in the revision log.
type TechCardRevision struct {
	Version      sql.NullString `db:"version"`
	RevisionDate sql.NullTime   `db:"revision_date"`
	Author       sql.NullString `db:"author"`
	Section      sql.NullString `db:"section"`
	ChangeNote   sql.NullString `db:"change_note"`
}

// TechCardInsert is the writable payload for a tech card (header + construction
// description + child sections). Child slices are full replacements on update.
type TechCardInsert struct {
	StyleNumber       string              `db:"style_number"`
	Name              string              `db:"name"`
	Brand             sql.NullString      `db:"brand"`
	Season            sql.NullString      `db:"season"`
	Collection        sql.NullString      `db:"collection"`
	CategoryId        sql.NullInt32       `db:"category_id"`
	TargetGender      sql.NullString      `db:"target_gender"`
	Stage             TechCardStage       `db:"stage"`
	Status            sql.NullString      `db:"status"`
	Version           sql.NullString      `db:"version"`
	RevisionDate      sql.NullTime        `db:"revision_date"`
	BaseModelId       sql.NullInt32       `db:"base_model_id"`
	BaseSampleSizeId  sql.NullInt32       `db:"base_sample_size_id"`
	Designer          sql.NullString      `db:"designer"`
	Constructor       sql.NullString      `db:"constructor"`
	Technologist      sql.NullString      `db:"technologist"`
	TargetCost        decimal.NullDecimal `db:"target_cost"`
	TargetRetailPrice decimal.NullDecimal `db:"target_retail_price"`
	Currency          sql.NullString      `db:"currency"`
	// construction description
	Description  sql.NullString `db:"description"`
	Silhouette   sql.NullString `db:"silhouette"`
	Collar       sql.NullString `db:"collar"`
	Fastening    sql.NullString `db:"fastening"`
	Pockets      sql.NullString `db:"pockets"`
	SleeveCuff   sql.NullString `db:"sleeve_cuff"`
	ExtraDetails sql.NullString `db:"extra_details"`
	Topstitching sql.NullString `db:"topstitching"`
	AuxMaterials sql.NullString `db:"aux_materials"`
	Notes        sql.NullString `db:"notes"`
	// child sections (in-memory only; persisted to their own tables)
	SizeIds    []int               `db:"-"`
	ProductIds []int               `db:"-"`
	Media      []TechCardMediaItem `db:"-"`
	Callouts   []TechCardCallout   `db:"-"`
	Revisions  []TechCardRevision  `db:"-"`
}

// TechCardListFilter holds optional filters for listing tech cards. Empty/zero
// fields mean "no filter".
type TechCardListFilter struct {
	Stage     string // tech_card.stage exact match
	Gender    string // tech_card.target_gender exact match
	Brand     string // case-insensitive substring on brand
	Season    string // case-insensitive substring on season
	Name      string // case-insensitive substring on name or style_number
	ProductId int    // only cards linked to this product
}

// TechCard is a stored tech card (tech_card row + child sections + resolved media).
type TechCard struct {
	Id int `db:"id"`
	TechCardInsert
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
	// ResolvedMedia carries the sketch media with their MediaFull resolved.
	ResolvedMedia []TechCardMediaFull `db:"-"`
}

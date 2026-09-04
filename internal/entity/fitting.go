package entity

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// ErrFittingConflict is returned by UpdateFitting when the caller's expected lock_version does not
// match the stored one (S25) — a concurrent edit landed between the read and the save. The caller
// should reload and retry (mirrors ErrTechCardConflict). ABORTED upstream.
var ErrFittingConflict = errors.New("fitting was modified concurrently")

// FittingChangeStatus is the resolution state of a structured change-request item (S26).
const (
	FittingChangeStatusOpen     = "open"
	FittingChangeStatusResolved = "resolved"
)

// ValidFittingChangeStatuses is the closed set of change-request statuses (DB CHECK + dto).
var ValidFittingChangeStatuses = map[string]bool{
	FittingChangeStatusOpen: true, FittingChangeStatusResolved: true,
}

// ValidFittingChangeZones is the fitting-owned vocabulary of garment AREAS a change request can point
// at. It started as a mirror of the tech_card_operation.zone dictionary (0076), but that one groups
// SEWING operations into construction bands (outer/lining/interlining) — a fitting remark is about
// where on the GARMENT the problem is ("рукав короткий", "сидит по плечу"), which those three bands
// can't express. The bands are kept (a remark can genuinely be about the lining as a layer) and the
// areas are added alongside; the two dictionaries are now independent by design.
//
// `unknown` is the legacy no-op token, equivalent to an unset zone; it is accepted on write for old
// clients but never produced by the current UI.
// The slice is the source of truth (a Go map iterates randomly, which would make the same rejection
// print its list in a different order every time); ValidFittingChangeZones is derived from it, so the
// two cannot drift. Keep it in reading order — the admin's picker follows the same grouping.
var fittingChangeZoneTokens = []string{
	"unknown", // legacy no-op, equivalent to unset
	// material bands (the original tech_card_operation.zone set)
	"outer", "lining", "interlining",
	// garment areas
	"sleeve", "collar", "neckline", "armhole", "shoulder", "chest", "waist", "hip", "hem",
	"pocket", "closure", "back", "front",
	"other",
}

var ValidFittingChangeZones = func() map[string]bool {
	m := make(map[string]bool, len(fittingChangeZoneTokens))
	for _, z := range fittingChangeZoneTokens {
		m[z] = true
	}
	return m
}()

// FittingChangeZoneTokens lists the accepted zone tokens in a stable order.
func FittingChangeZoneTokens() []string {
	return append([]string(nil), fittingChangeZoneTokens...)
}

// FittingChangeZonePrefixes are enum-name prefixes a client may send instead of the bare token
// (the admin reused the TECH_CARD_CONSTRUCTION_ZONE_* proto enum for this field before it grew its
// own dictionary). NormalizeFittingChangeZone strips them so an older client is normalised, not 400'd.
var FittingChangeZonePrefixes = []string{
	"TECH_CARD_CONSTRUCTION_ZONE_",
	"FITTING_CHANGE_ZONE_",
	"FITTING_ZONE_",
}

// NormalizeFittingChangeZone maps a wire zone value onto its dictionary token: trimmed, lowercased,
// with any known enum-name prefix removed. The legacy `unknown` collapses to "" (unset) so the two
// spellings of "no zone" do not both end up in storage. The result is NOT validated here — the caller
// checks it against ValidFittingChangeZones.
func NormalizeFittingChangeZone(zone string) string {
	z := strings.TrimSpace(zone)
	for _, p := range FittingChangeZonePrefixes {
		if len(z) > len(p) && strings.EqualFold(z[:len(p)], p) {
			z = z[len(p):]
			break
		}
	}
	z = strings.ToLower(z)
	if z == "unknown" {
		return ""
	}
	return z
}

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

// FittingPattern is a cut-pattern (выкройка) iteration measured in a fitting — PDF or
// DXF, told apart by the url's extension. It is a snapshot of the uploaded file (url +
// filename), not a live reference to a tech-card pattern — the tech card holds the final
// pattern, a fitting captures the iteration tried.
type FittingPattern struct {
	SizeId   sql.NullInt32  `db:"size_id"`
	URL      string         `db:"url"`
	Filename sql.NullString `db:"filename"`
	// Name is the operator-entered display name. Same write semantics as
	// TechCardSizePattern.Name — Valid=false means absent from the payload (carry the stored
	// name forward by (size_id, url)); Valid=true writes as given, empty clearing to NULL.
	Name      sql.NullString `db:"name"`
	SizeBytes sql.NullInt64  `db:"size_bytes"`
}

// FittingCallout is a numbered marker pinned to a fitting photo, flagging a fit
// problem at a point on the image. No part/dimensions (a fitting binds its remarks to
// pieces through fitting_change_request, not by a name on the callout).
//
// Геометрия (Kind/Points/Color/Dashed/Filled) — ТОТ ЖЕ примитив, что у TechCardCallout, теми же
// типами и с теми же правилами числа точек: замечание примерки переносят в тех-карту, и дуга
// обязана остаться той же дугой по обе стороны переноса. PosX/PosY остаются положением
// НУМЕРОВАННОГО МАРКЕРА — по номеру на выноску ссылается FittingChangeRequest.CalloutNumber, —
// а Points держит якоря фигуры; у пина Points пуст. Колонки заведены 0319.
type FittingCallout struct {
	Number  int                     `db:"callout_number"`
	Note    sql.NullString          `db:"note"`
	MediaId sql.NullInt32           `db:"media_id"` // the fitting photo this callout is pinned to
	PosX    decimal.NullDecimal     `db:"pos_x"`    // normalised 0..1 marker position
	PosY    decimal.NullDecimal     `db:"pos_y"`
	Kind    TechCardAnnotationKind  `db:"kind"`
	Color   TechCardAnnotationColor `db:"color"`
	Dashed  bool                    `db:"dashed"`
	Filled  bool                    `db:"filled"`
	// Caps — наконечники линии (0362). Тот же примитив и та же колонка, что у карточной выноски:
	// замечание примерки переносят в тех-карту, и стрелка обязана остаться стрелкой.
	Caps TechCardAnnotationCaps `db:"caps"`
	// KindOmitted — вкладка со старым бандлом про геометрию не говорила вовсе. Тогда хранимая
	// группа (вид, якоря, цвет, пунктир, штриховка) переносится по НОМЕРУ выноски, до записи. Не
	// колонка: это факт запроса, а не примерки.
	KindOmitted bool `db:"-"`
	// Points в БД лежит JSON-колонкой, в Go — разобранным списком; сырое значение читается
	// в PointsRaw и разбирается стором один раз (так же, как у карточной выноски).
	Points    []TechCardAnnotationPoint `db:"-"`
	PointsRaw []byte                    `db:"points"`
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

// FittingChangeRequest is one structured remark item produced by a fitting (S26, §2.7). target is the
// change category; Zone + PieceIds are the structured location; Status (open|resolved) replaces the old
// boolean resolved; CarriedFromId links this item to the prior-round item it continues. Managed via the
// dedicated change-request CRUD so its id is STABLE (carry-over depends on it); an initial batch may
// still be supplied on AddFitting. FittingId/RoundNumber are read context (RoundNumber is populated
// only on the carry-over projection).
type FittingChangeRequest struct {
	Id            int            `db:"id"`
	FittingId     int            `db:"fitting_id"`
	Target        string         `db:"target"`
	Note          string         `db:"note"`
	CalloutNumber sql.NullInt32  `db:"callout_number"`
	Zone          sql.NullString `db:"zone"`
	Status        string         `db:"status"`
	CarriedFromId sql.NullInt32  `db:"carried_from_id"`
	CreatedBy     string         `db:"created_by"`
	RoundNumber   sql.NullInt32  `db:"round_number"` // carry-over context (derived from the sample); not a column here
	// PieceIds are the tech_card_piece rows this remark is about (0..n). Stored in the
	// fitting_change_request_piece join table, not on the row — one remark routinely spans several
	// pieces. Empty = not pinned to a piece. The legacy single fitting_change_request.piece_id column
	// is read-only history: 0256 backfilled it into the join table and nothing writes it any more.
	PieceIds []int `db:"-"`
}

// FittingInsert is the writable payload for a fitting session. A fitting anchors
// to a tech card (the style) and/or a specific product (the colour/SKU sample);
// at least one of TechCardId / ProductId is set (enforced in the API layer).
type FittingInsert struct {
	TechCardId  sql.NullInt32  `db:"tech_card_id"`
	ProductId   sql.NullInt32  `db:"product_id"`
	ModelId     sql.NullInt32  `db:"model_id"`
	FittingDate time.Time      `db:"fitting_date"`
	Comment     sql.NullString `db:"comment"`
	Status      FittingStatus  `db:"status"`
	Verdict     FittingVerdict `db:"verdict"`
	RoundNumber sql.NullInt32  `db:"round_number"` // legacy per-card try-on #; the authoritative round is now the sample's (§2.7)
	Outcome     sql.NullString `db:"outcome"`      // FittingOutcome; NULL = undecided
	SampleId    sql.NullInt32  `db:"sample_id"`    // the sample this fitting tried on — the primary anchor (§2.7)
	// Audit stamps (§2.11): server-set from the JWT (replaces the deprecated client-supplied recorded_by).
	CreatedBy      string                 `db:"created_by"`
	UpdatedBy      string                 `db:"updated_by"`
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
	LockVersion int         `db:"lock_version"` // optimistic-lock counter (S25); echoed on UpdateFitting
	Media       []MediaFull `db:"-"`
	CreatedAt   time.Time   `db:"created_at"`
	UpdatedAt   time.Time   `db:"updated_at"`
}

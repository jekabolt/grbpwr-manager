package entity

import (
	"database/sql"
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// ErrProductionRunCancelledReceive is returned by the receipt command when the run is cancelled: a
// cancelled batch has no goods to book, and receiving it would resurrect its plan as stock. (The
// old receive path never guarded this — only received/closed — so a cancelled run could be
// received; the receipt command closes that hole.)
var ErrProductionRunCancelledReceive = errors.New("a cancelled production run cannot be received")

// ErrProductionRunReceiptLineUnknown is returned by the receipt command when a submitted line_key
// names no plan line of the run — the client counted against a stale grid. Reload and retry.
var ErrProductionRunReceiptLineUnknown = errors.New("a receipt line names no plan line of this run; reload and retry")

// ErrIdempotencyConflict is returned when a command retries an idempotency key with a DIFFERENT
// payload than the one that executed: the client reused a key across two distinct intents. The
// original command's effects stand; the divergent retry is refused, never merged.
var ErrIdempotencyConflict = errors.New("idempotency key was already used with a different payload")

// CommandTypeProductionRunReceipt is the command_idempotency.command_type of the receipt command.
const CommandTypeProductionRunReceipt = "production_run_receipt"

// Receipt posting_status values (the accounting-outbox lifecycle of a receipt).
const (
	ReceiptPostingPending    = "pending"
	ReceiptPostingPosted     = "posted"
	ReceiptPostingDeadLetter = "dead_letter"
)

// ProductionRunReceiptLine is one counted plan line of a receipt: the plan line it was counted
// against (RunLineId — the FK that 0230's stable ids exist for), a snapshot of that line's
// product/size at receipt time, and the disjoint good/defect counts. GoodQty is what was posted to
// stock; DefectQty is recorded, never stocked.
type ProductionRunReceiptLine struct {
	Id        int           `db:"id"`
	ReceiptId int           `db:"receipt_id"`
	RunLineId int           `db:"run_line_id"`
	ProductId sql.NullInt32 `db:"product_id"`
	SizeId    sql.NullInt32 `db:"size_id"`
	GoodQty   int           `db:"good_qty"`
	DefectQty int           `db:"defect_qty"`
	// LineKey is the plan line's stable identity, joined from production_run_line on read so the
	// client can correlate the receipt with its grid without ever seeing row ids.
	LineKey string `db:"line_key"`
}

// ProductionRunReceipt is one immutable receiving event of a run (Phase 4, receipt v1): who
// received what and when, at what frozen valuation. UnitCostBase is the run's actual unit cost the
// moment the goods were booked — later costing edits never change it; NULL (HasBase false) means it
// was not computable then (uncosted issues / unfolded articles) and is honestly absent.
type ProductionRunReceipt struct {
	Id             int                 `db:"id"`
	RunId          int                 `db:"run_id"`
	ReceivedAt     time.Time           `db:"received_at"`
	AdminUsername  sql.NullString      `db:"admin_username"`
	Note           sql.NullString      `db:"note"`
	IdempotencyKey string              `db:"idempotency_key"`
	UnitCostBase   decimal.NullDecimal `db:"unit_cost_base"`
	BaseCurrency   sql.NullString      `db:"base_currency"`
	HasBase        bool                `db:"has_base"`
	ReversalOf     sql.NullInt32       `db:"reversal_of"`
	ReversedBy     sql.NullInt32       `db:"reversed_by"`
	PostingStatus  string              `db:"posting_status"`
	// Final marks the receipt that declared the run complete and flipped it to received (Phase 5).
	// Every pre-Phase-5 receipt is final by construction (0246 backfill).
	Final     bool      `db:"final"`
	CreatedAt time.Time `db:"created_at"`
	Lines     []ProductionRunReceiptLine
}

// ProductionRunReceiptLineInput is one line of the receipt command as submitted: the plan line's
// stable line_key and the disjoint counts.
type ProductionRunReceiptLineInput struct {
	LineKey   string
	GoodQty   int
	DefectQty int
}

// PostProductionRunReceiptParams is the receipt command (Phase 4, final-only). RequestHash is the
// SHA-256 of the canonical payload, computed at the DTO boundary so the store compares, never
// re-derives. ValidProducts/ValidSizes carry the handler's tech-card validation into the
// transaction: the store re-checks the FRESH plan lines against them under the run lock, so a line
// edit racing the command cannot book stock into a product the handler never validated. Empty maps
// skip that check (the aux path validates product-lessness instead).
type PostProductionRunReceiptParams struct {
	RunID               int
	Lines               []ProductionRunReceiptLineInput
	IdempotencyKey      string
	RequestHash         string
	ExpectedLockVersion int
	Note                string
	UpdateCostPrice     bool
	Username            string
	// BaseCurrency labels the frozen valuation (the handler passes the configured base currency —
	// the store does not read config/cache).
	BaseCurrency  string
	ValidProducts map[int]bool
	ValidSizes    map[int]bool
	// Aux marks an auxiliary run (tech card purpose=auxiliary): good units are booked into
	// OutputMaterialID in the material warehouse instead of product stock, and every line must be
	// product-less.
	Aux              bool
	OutputMaterialID int
	// Final declares the run complete: the run flips to received and further receipts are refused.
	// False books a partial delivery and moves the run to partially_received (Phase 5). Part of the
	// request hash — the same idempotency key with a flipped flag is a different intent.
	Final bool
}

// PostProductionRunReceiptResult is what the receipt command returns — and what a replayed retry
// returns verbatim from the idempotency record.
type PostProductionRunReceiptResult struct {
	ReceiptID        int  `json:"receipt_id"`
	CostPriceUpdated bool `json:"cost_price_updated"`
	Replayed         bool `json:"-"`
}

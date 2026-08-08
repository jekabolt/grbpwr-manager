package entity

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Ф5б.4 + Ф5б.6 — the material reservation ledger's SECOND owner: a production run holding fabric.
//
// Until 0286 the ledger (0164) had exactly one kind of owner, a customer order reserving packaging.
// Fabric was reserved by nobody, so two runs on the same cloth both read "there is enough". The
// ledger now admits a run as owner (order_id NULLable, run_id added, CHECK XOR between the two), and
// the types below are the Go side of that second owner.
//
// The load-bearing consequence, and the reason this is an extension rather than a second table:
// openReservedQty sums a material's open claims WITHOUT looking at who owns them, so a run's hold
// starts depressing available(material) the moment it is written — no reader had to be taught about
// runs. If a reader had needed teaching, one number would have been split into two half-answers and
// the first caller to forget the second half would sell what is already held.

// ErrMaterialLotNotFound is returned when a per-lot availability read names a lot that does not
// exist. Per-lot availability is asked ONLY by the recut check (Р7), and there a missing lot is a
// caller bug, not an empty result.
var ErrMaterialLotNotFound = errors.New("material lot not found")

// runClaimKeyPrefix separates the run claim namespace from the order one. Without it a run 7 and an
// order 7 claiming the same material would produce the same claim_key, and the second claim would
// collapse silently into the first's INSERT IGNORE — a hold that reads as already recorded and is
// therefore never written, never closed, and never visible.
const runClaimKeyPrefix = "run"

// RunReservationClaimKey is the deterministic idempotency root of a run's claim on one material:
//
//	run:{run_id}:{material_id}:{generation}
//
// The generation exists because the ledger is append-only and UNIQUE(claim_key, event) physically
// forbids a second 'reserve' on one key. A correction is therefore NOT an UPDATE of the held
// quantity — it is a 'release' of the current generation followed by a 'reserve' of the next. Every
// step stays a no-op on repeat, because the key of a given generation is fixed.
//
// The ORDER claim key (PackagingClaimKey, "{order_id}:{material_id}") is deliberately NOT rewritten
// into this shape: it has live rows, and rewriting an idempotency key makes every claim already open
// under the old key invisible to the code that closes it — a permanent, silent hold.
func RunReservationClaimKey(runID, materialID, generation int) string {
	return fmt.Sprintf("%s:%d:%d:%d", runClaimKeyPrefix, runID, materialID, generation)
}

// ParseRunReservationGeneration reads the generation back out of a run claim key. It reports false
// for anything that is not a run claim key of the current shape (an order key, a hand-written row, a
// future format), so a foreign key can never be mistaken for generation 0 and silently reused.
func ParseRunReservationGeneration(claimKey string) (int, bool) {
	parts := strings.Split(claimKey, ":")
	if len(parts) != 4 || parts[0] != runClaimKeyPrefix {
		return 0, false
	}
	gen, err := strconv.Atoi(parts[3])
	if err != nil || gen < 0 {
		return 0, false
	}
	return gen, true
}

// RunMaterialRequirement is one line of what a run holds: how much of a material, and — only for a
// recut (Ф5б.6) — which lot it must come from. A recut a month later out of a different dye lot is
// visible on the finished garment as a defect, so the cloth for it is held on the SAME lot the
// original cut came from.
//
// LotId does NOT change how available(material) is computed: a lot-pinned claim is still a hold on
// that material and is counted like any other (Р7). It only makes the separate per-lot question
// answerable — "how much of lot X is still free" — which is asked by the recut check alone.
type RunMaterialRequirement struct {
	Qty   decimal.Decimal
	LotId sql.NullInt32
}

// MaterialReservationClaim is one OPEN claim on a material, with its owner and its age. It is the
// read model behind "who is holding this cloth, and since when".
//
// CreatedAt is not decoration. A run abandoned in `planned` holds its fabric until somebody closes
// or cancels it, and that is a visible consequence of the design rather than a bug to be hidden: the
// hold is real, and the only honest way to surface it is to say how long it has been standing.
//
// OrderId and RunId are exclusive by DB CHECK (chk_material_reservation_owner_xor, 0286): exactly
// one is set. A claim with neither would be a hold nobody can close — it would press on available
// forever, released by no order closing and no run closing.
type MaterialReservationClaim struct {
	Id         int             `db:"id"`
	MaterialId int             `db:"material_id"`
	OrderId    sql.NullInt32   `db:"order_id"`
	RunId      sql.NullInt32   `db:"run_id"`
	LotId      sql.NullInt32   `db:"lot_id"`
	Qty        decimal.Decimal `db:"qty"`
	ClaimKey   string          `db:"claim_key"`
	CreatedBy  string          `db:"created_by"`
	CreatedAt  time.Time       `db:"created_at"`
}

// Age reports how long the claim has been open as of now. The caller passes the clock (the store's
// Now) so the value is testable and consistent with the rest of the store's time handling.
func (c MaterialReservationClaim) Age(now time.Time) time.Duration {
	if c.CreatedAt.IsZero() {
		return 0
	}
	return now.Sub(c.CreatedAt)
}

// MaterialLotAvailability answers the ONE question that needs a lot rather than a material: how much
// of this specific lot is still free to be held for a recut (Р7).
//
//	Available = material_lot.remaining_qty − Σ qty of OPEN claims naming this lot
//
// This is a separate read, called by the recut check only. It is deliberately NOT folded into the
// general available(material) path: the general question is about the material — how much cloth of
// this article do we have — and answering it per roll would refuse a run that has plenty of cloth
// spread across several lots.
type MaterialLotAvailability struct {
	LotId        int
	MaterialId   int
	LotCode      string
	RemainingQty decimal.Decimal
	Reserved     decimal.Decimal
	Available    decimal.Decimal
}

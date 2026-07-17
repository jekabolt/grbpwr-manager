package entity

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Packaging / reservation errors (PLM rework Q3 / S22, 01-DOMAIN-MODEL §2.8).
var (
	// ErrMaterialReserved is returned by a manual stock adjustment that would drive on-hand below the
	// quantity already reserved for open orders (an oversell of packaging). The message carries the
	// reserved and free quantities so the API can surface them.
	ErrMaterialReserved = errors.New("material has open reservations")
	// ErrPackagingRecipeInvalid is returned when a packaging recipe payload is malformed (bad scope,
	// scope↔key mismatch, missing material, negative qty).
	ErrPackagingRecipeInvalid = errors.New("invalid packaging recipe")
)

// PackagingRecipeScope selects what a packaging recipe applies to. Resolution at order time is
// most-specific-first: a product-scope recipe wins over a style-scope one, which wins over the
// global fallback (Q3 "product → style → global, first match wins").
type PackagingRecipeScope string

const (
	PackagingScopeGlobal  PackagingRecipeScope = "global"  // fallback for any order (was the flat packaging_bom)
	PackagingScopeStyle   PackagingRecipeScope = "style"   // tech_card_id set
	PackagingScopeProduct PackagingRecipeScope = "product" // product_id set
)

// ValidPackagingRecipeScopes is the closed set enforced by the DB CHECK and validated in the dto.
var ValidPackagingRecipeScopes = map[PackagingRecipeScope]struct{}{
	PackagingScopeGlobal: {}, PackagingScopeStyle: {}, PackagingScopeProduct: {},
}

// PackagingRecipe is one line of a packaging configuration: a material consumed on ship,
// QtyPerOrder once per shipment (a box) plus QtyPerItem × the order's unit count (a dust bag), for a
// given scope target. MaterialName/MaterialUnit are resolved on read (List) for display; they are
// ignored on write.
type PackagingRecipe struct {
	Id           int                  `db:"id"`
	Scope        PackagingRecipeScope `db:"scope"`
	TechCardId   sql.NullInt32        `db:"tech_card_id"` // set iff scope=style
	ProductId    sql.NullInt32        `db:"product_id"`   // set iff scope=product
	MaterialId   int                  `db:"material_id"`
	MaterialName string               `db:"material_name"`
	MaterialUnit sql.NullString       `db:"material_unit"`
	QtyPerOrder  decimal.Decimal      `db:"qty_per_order"`
	QtyPerItem   decimal.Decimal      `db:"qty_per_item"`
	Active       bool                 `db:"active"`
	LockVersion  int                  `db:"lock_version"`
	CreatedBy    string               `db:"created_by"`
	UpdatedBy    string               `db:"updated_by"`
}

// PackagingRecipeInsert is one line of a packaging recipe write (full-replace per scope target). The
// scope target (TechCardId / ProductId) is carried by the UpsertPackagingRecipe call, not per line.
type PackagingRecipeInsert struct {
	MaterialId  int
	QtyPerOrder decimal.Decimal
	QtyPerItem  decimal.Decimal
	Active      bool
}

// MaterialReservationEvent is a packaging claim's lifecycle step. A claim is OPEN while it has a
// reserve row and no consume/release row.
type MaterialReservationEvent string

const (
	MaterialReservationReserve MaterialReservationEvent = "reserve" // at order placement
	MaterialReservationConsume MaterialReservationEvent = "consume" // at ship (fulfils the claim)
	MaterialReservationRelease MaterialReservationEvent = "release" // at cancel / refund
)

// ValidMaterialReservationEvents is the closed set enforced by the DB CHECK.
var ValidMaterialReservationEvents = map[MaterialReservationEvent]struct{}{
	MaterialReservationReserve: {}, MaterialReservationConsume: {}, MaterialReservationRelease: {},
}

// MaterialReservation is one append-only row of the packaging reservation ledger (§2.8, S22). It
// never moves on_hand — the physical decrement stays the ship-time material_stock_movement writeoff;
// this ledger only tracks whether a claim is still open, so available = on_hand − Σ open.
type MaterialReservation struct {
	Id         int                      `db:"id"`
	MaterialId int                      `db:"material_id"`
	OrderId    int                      `db:"order_id"`
	Qty        decimal.Decimal          `db:"qty"`
	Event      MaterialReservationEvent `db:"event"`
	ClaimKey   string                   `db:"claim_key"`
	CreatedBy  string                   `db:"created_by"`
	CreatedAt  time.Time                `db:"created_at"`
}

// PackagingClaimKey is the deterministic idempotency root for a (order, material) packaging claim:
// a repeated reserve/consume/release for the same claim collapses to one row via UNIQUE(claim_key,
// event), so retries are no-ops (mirrors the order_packaging_consumed PK-claim of 0116).
func PackagingClaimKey(orderID, materialID int) string {
	return fmt.Sprintf("%d:%d", orderID, materialID)
}

// MaterialAvailability is a material's soft-availability view: physical on-hand minus the quantity
// reserved for open orders. Available can go negative (packaging oversold) — that is surfaced, not
// blocked, because a sale must never fail on packaging; a manual adjustment, however, refuses to
// deepen an oversell (ErrMaterialReserved).
type MaterialAvailability struct {
	MaterialId int
	OnHand     decimal.Decimal
	Reserved   decimal.Decimal
	Available  decimal.Decimal
}

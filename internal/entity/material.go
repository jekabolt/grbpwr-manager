package entity

import (
	"database/sql"
	"time"

	"github.com/shopspring/decimal"
)

// MaterialInsert is the editable payload of a catalog material — the shared nomenclature a
// tech-card BOM line can optionally link to. It mirrors the descriptive (non-price) fields of
// tech_card_bom_item; price lives in the append-only MaterialPrice history, not here.
type MaterialInsert struct {
	Name            string              `db:"name" valid:"required"`
	Section         string              `db:"section" valid:"required"`
	Supplier        sql.NullString      `db:"supplier" valid:"-"`
	SupplierRef     sql.NullString      `db:"supplier_ref" valid:"-"`
	Composition     sql.NullString      `db:"composition" valid:"-"`
	Spec            sql.NullString      `db:"spec" valid:"-"`
	Unit            sql.NullString      `db:"unit" valid:"-"`
	FabricWidth     decimal.NullDecimal `db:"fabric_width" valid:"-"`
	FabricWeightGsm decimal.NullDecimal `db:"fabric_weight_gsm" valid:"-"`
	// Warehouse catalog fields (NF-02).
	Code     sql.NullString      `db:"code" valid:"-"`      // internal article code (ours), unique among non-archived
	Color    sql.NullString      `db:"color" valid:"-"`     // colour of the purchased article
	Pantone  sql.NullString      `db:"pantone" valid:"-"`   // pantone reference
	MinStock decimal.NullDecimal `db:"min_stock" valid:"-"` // low-stock alert threshold, in Unit
	Notes    sql.NullString      `db:"notes" valid:"-"`
}

// Material is a catalog material with its lifecycle columns.
type Material struct {
	Id int `db:"id"`
	MaterialInsert
	Archived  bool      `db:"archived"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// MaterialPriceSource enumerates how a price point entered the history.
const (
	MaterialPriceSourceManual        = "manual"
	MaterialPriceSourceProductionRun = "production_run"
)

// MaterialPrice is one point in a material's append-only price history. The latest row with
// valid_from <= today (per currency) is the current price. Prices are stored in the purchase
// currency and folded to base via costing_fx_rate (see task 04).
type MaterialPrice struct {
	MaterialId int             `db:"material_id"`
	Price      decimal.Decimal `db:"price"`
	Currency   string          `db:"currency"`
	ValidFrom  time.Time       `db:"valid_from"`
	Source     string          `db:"source"`
	Note       sql.NullString  `db:"note"`
}

// MaterialWithPrice is a catalog material plus its current (latest-effective) price, if any.
// The list/detail read joins the most recent price so the admin UI can show and pre-fill it.
type MaterialWithPrice struct {
	Material
	LatestPrice *MaterialPrice
}

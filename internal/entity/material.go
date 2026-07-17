package entity

import (
	"database/sql"
	"time"

	"github.com/shopspring/decimal"
)

// MaterialClass is the class-table-inheritance discriminant (S15): it selects which typed
// side-table (if any) carries a material's structural attributes. Kept as a distinct ~string type
// so ValidMaterialClasses can be diff-checked against the DB CHECK (migrationlint) and the proto enum.
type MaterialClass string

const (
	MaterialClassFabric    MaterialClass = "fabric"
	MaterialClassHardware  MaterialClass = "hardware"
	MaterialClassThread    MaterialClass = "thread"
	MaterialClassPackaging MaterialClass = "packaging"
	MaterialClassOther     MaterialClass = "other" // unclassifiable; attributes live in the other_attrs JSON escape-hatch
)

// ValidMaterialClasses is the storable set — the single source of truth mirrored by the DB CHECK
// (chk_material_class, migration 0157) and the proto MaterialClass enum.
var ValidMaterialClasses = map[MaterialClass]bool{
	MaterialClassFabric:    true,
	MaterialClassHardware:  true,
	MaterialClassThread:    true,
	MaterialClassPackaging: true,
	MaterialClassOther:     true,
}

// MaterialFabricAttr are the typed attributes of a fabric-class material (material_fabric_attr).
type MaterialFabricAttr struct {
	WidthCm         decimal.NullDecimal `db:"width_cm"`
	WeightGsm       decimal.NullDecimal `db:"weight_gsm"`
	FabricDirection sql.NullString      `db:"fabric_direction"` // lengthwise|crosswise|any
	ShrinkagePct    decimal.NullDecimal `db:"shrinkage_pct"`
	RollLengthM     decimal.NullDecimal `db:"roll_length_m"`
}

// MaterialHardwareAttr are the typed attributes of a hardware-class material (material_hardware_attr).
type MaterialHardwareAttr struct {
	DiameterMm   decimal.NullDecimal `db:"diameter_mm"`
	Dimensions   sql.NullString      `db:"dimensions"`
	Finish       sql.NullString      `db:"finish"`
	BaseMaterial sql.NullString      `db:"base_material"`
	WeightG      decimal.NullDecimal `db:"weight_g"`
}

// MaterialThreadAttr are the typed attributes of a thread-class material (material_thread_attr).
// Fibre composition is NOT here — it lives in material_composition (structural, S17).
type MaterialThreadAttr struct {
	TicketTex      sql.NullString      `db:"ticket_tex"`
	LengthPerConeM decimal.NullDecimal `db:"length_per_cone_m"`
	NeedleReco     sql.NullString      `db:"needle_reco"`
}

// MaterialPackagingAttr are the typed attributes of a packaging-class material (material_packaging_attr).
type MaterialPackagingAttr struct {
	Substrate   sql.NullString      `db:"substrate"`
	Dimensions  sql.NullString      `db:"dimensions"`
	Gsm         decimal.NullDecimal `db:"gsm"`
	PrintMethod sql.NullString      `db:"print_method"`
}

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
	// CTI typing (S15). MaterialClass is the discriminant; exactly the matching typed attribute
	// pointer is populated (the rest nil); OtherAttrs is the JSON escape-hatch for class 'other'.
	// The attribute pointers are not base columns (db:"-") — they are loaded from / written to the
	// side-tables separately. An empty MaterialClass is normalised to 'other' on write.
	MaterialClass string                 `db:"material_class" valid:"-"`
	FabricAttr    *MaterialFabricAttr    `db:"-" valid:"-"`
	HardwareAttr  *MaterialHardwareAttr  `db:"-" valid:"-"`
	ThreadAttr    *MaterialThreadAttr    `db:"-" valid:"-"`
	PackagingAttr *MaterialPackagingAttr `db:"-" valid:"-"`
	OtherAttrs    []byte                 `db:"other_attrs" valid:"-"` // JSON; only for class 'other'
	// CompositionEntries is the material's structured fibre composition (S17, material_composition):
	// each fibre's percent share, summing to 100 when set. Not a base column (db:"-") — it is written
	// to / read from the material_composition side-table separately. Empty means "no composition".
	CompositionEntries []CompositionEntry `db:"-" valid:"-"`
	// Username audit stamps (server-set from the JWT, no FK). CreatedBy is written once on create;
	// UpdatedBy on every write.
	CreatedBy string `db:"created_by" valid:"-"`
	UpdatedBy string `db:"updated_by" valid:"-"`
}

// Material is a catalog material with its lifecycle columns.
type Material struct {
	Id int `db:"id"`
	MaterialInsert
	Archived bool `db:"archived"`
	// LockVersion is the optimistic-lock counter (S25). UpdateMaterial requires the caller to echo
	// the version it read and bumps it on success; a stale echo yields ErrMaterialConflict.
	LockVersion int       `db:"lock_version"`
	CreatedAt   time.Time `db:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"`
}

// MaterialPriceSource enumerates how a price point entered the history. (MaterialPriceSourcePurchase
// lives in inventory.go next to the receipt path that writes it.)
const (
	MaterialPriceSourceManual        = "manual"
	MaterialPriceSourceProductionRun = "production_run"
)

// ValidMaterialPriceSources is the storable set for material_price.source — the single source of
// truth mirrored by the DB CHECK (chk_material_price_source, migration 0158). source was previously
// the only PLM field with no validation at all (A3.4), so a typo silently entered the append-only
// price history.
var ValidMaterialPriceSources = map[string]bool{
	MaterialPriceSourceManual:        true,
	MaterialPriceSourceProductionRun: true,
	MaterialPriceSourcePurchase:      true,
}

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

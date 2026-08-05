package entity

import (
	"database/sql"
	"strings"
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

// MaterialPurpose (#40) marks whether a catalog material is used for samples, production, or both —
// so the admin can mark and filter. Kept as a distinct ~string type mirrored by the DB CHECK
// (chk_material_purpose, migration 0184) and the proto MaterialPurpose enum.
type MaterialPurpose string

const (
	MaterialPurposeSample     MaterialPurpose = "sample"
	MaterialPurposeProduction MaterialPurpose = "production"
	MaterialPurposeBoth       MaterialPurpose = "both"
)

// ValidMaterialPurposes is the storable set — the single source of truth mirrored by the DB CHECK
// (chk_material_purpose, migration 0184) and the proto MaterialPurpose enum. An empty/unknown purpose
// is normalised to 'both' on write (a material serves both flows unless explicitly narrowed).
var ValidMaterialPurposes = map[MaterialPurpose]bool{
	MaterialPurposeSample:     true,
	MaterialPurposeProduction: true,
	MaterialPurposeBoth:       true,
}

// MaterialFabricAttr are the typed attributes of a fabric-class material (material_fabric_attr).
type MaterialFabricAttr struct {
	// WidthCm is the FULL roll width. The usable cutting width derives from it minus the selvedge
	// on both edges — see Material.UsableFabricWidthCm.
	WidthCm         decimal.NullDecimal `db:"width_cm"`
	WeightGsm       decimal.NullDecimal `db:"weight_gsm"`
	FabricDirection sql.NullString      `db:"fabric_direction"` // lengthwise|crosswise|any
	ShrinkagePct    decimal.NullDecimal `db:"shrinkage_pct"`
	RollLengthM     decimal.NullDecimal `db:"roll_length_m"`
	// SelvedgeCm is the unusable кромка per EDGE in cm (0259). A roll property, entered when the
	// material is added; 0 = none/unknown, keeping legacy behaviour bit-for-bit.
	SelvedgeCm decimal.Decimal `db:"selvedge_cm"`
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
	Name        string         `db:"name" valid:"required"`
	Section     string         `db:"section" valid:"required"`
	Supplier    sql.NullString `db:"supplier" valid:"-"`
	SupplierRef sql.NullString `db:"supplier_ref" valid:"-"`
	// SupplierId links the material to the supplier CATALOG (0201) — the FK the free-text Supplier
	// field never was (plan 13 §1; the PO entity was cut, so v1 is one blended supplier per
	// material). LeadTimeDays is its typical order-to-door time, feeding "when can a run start".
	SupplierId      sql.NullInt64       `db:"supplier_id" valid:"-"`
	LeadTimeDays    sql.NullInt64       `db:"lead_time_days" valid:"-"`
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
	// ImageId is an optional catalog image (#39): FK media(id), NULL when unset. Not resolved here —
	// the resolved MediaFull is attached on read as Material.Image.
	ImageId sql.NullInt32 `db:"image_id" valid:"-"`
	// Purpose marks whether the material is used for samples, production, or both (#40). An empty
	// value is normalised to 'both' on write (see normalizeMaterialPurpose).
	Purpose string `db:"purpose" valid:"-"`
	// CTI typing (S15). MaterialClass is the discriminant; exactly the matching typed attribute
	// pointer is populated (the rest nil); OtherAttrs is the JSON escape-hatch for class 'other'.
	// The attribute pointers are not base columns (db:"-") — they are loaded from / written to the
	// side-tables separately. An empty MaterialClass defaults to 'other' on create and means
	// "preserve the stored class" on update.
	MaterialClass string                 `db:"material_class" valid:"-"`
	FabricAttr    *MaterialFabricAttr    `db:"-" valid:"-"`
	HardwareAttr  *MaterialHardwareAttr  `db:"-" valid:"-"`
	ThreadAttr    *MaterialThreadAttr    `db:"-" valid:"-"`
	PackagingAttr *MaterialPackagingAttr `db:"-" valid:"-"`
	OtherAttrs    []byte                 `db:"other_attrs" valid:"-"` // JSON; only for class 'other'
	// CompositionEntries is the material's structured fibre composition (S17, material_composition):
	// each fibre's percent share, summing to 100 when set. Not a base column (db:"-") — it is written
	// to / read from the material_composition side-table separately. Empty means no composition on
	// create and "preserve stored composition" on update (the proto repeated field has no presence).
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
	// Image is the resolved catalog image (#39), attached on read from ImageId. Nil when unset.
	Image *MediaFull `db:"-"`
}

// EffectiveFabricWidthCm resolves the material's FULL roll width with the same CTI-over-flat
// preference the article-code generator uses (materialcode.preferredDecimal): the typed
// material_fabric_attr.width_cm wins when the side-table row exists, the legacy flat
// material.fabric_width is the fallback. Invalid when neither is set.
func (m *Material) EffectiveFabricWidthCm() decimal.NullDecimal {
	// Same fallback rule as preferredDecimal: an unset or ZERO typed width falls through to the
	// legacy flat column (zero is how a half-filled CTI row says "not really set").
	if m.FabricAttr != nil && m.FabricAttr.WidthCm.Valid && !m.FabricAttr.WidthCm.Decimal.IsZero() {
		return m.FabricAttr.WidthCm
	}
	return m.FabricWidth
}

// FabricSelvedgeCm is the кромка per edge in cm; zero when the material has no typed fabric
// attributes (the flat model never carried a selvedge).
func (m *Material) FabricSelvedgeCm() decimal.Decimal {
	if m.FabricAttr != nil {
		return m.FabricAttr.SelvedgeCm
	}
	return decimal.Zero
}

// UsableFabricWidthCm is the cutting width: full roll width minus the selvedge on BOTH edges,
// clamped at zero (a selvedge wider than the roll is operator error the read path must not turn
// into a negative width). Invalid when no width is known at all.
func (m *Material) UsableFabricWidthCm() decimal.NullDecimal {
	w := m.EffectiveFabricWidthCm()
	if !w.Valid {
		return w
	}
	usable := w.Decimal.Sub(m.FabricSelvedgeCm().Mul(decimal.NewFromInt(2)))
	if usable.IsNegative() {
		usable = decimal.Zero
	}
	return decimal.NullDecimal{Decimal: usable, Valid: true}
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

// MaterialWithPrice is a catalog material plus its current (latest-effective) prices. LatestPrices
// keeps one current row per currency for costing; LatestPrice is the backwards-compatible singular
// admin projection (the base-currency row when present, otherwise the newest cross-currency quote).
type MaterialWithPrice struct {
	Material
	LatestPrice  *MaterialPrice
	LatestPrices map[string]*MaterialPrice
}

// LatestPriceForCurrencies returns the first current price matching the supplied currency priority.
// If none matches, a sole available currency is unambiguous and is returned. Multiple unmatched
// currencies deliberately return nil: silently choosing one would make costing depend on row order.
// LatestPrice remains a fallback for older in-memory callers/tests that construct only the legacy
// singular field.
func (m MaterialWithPrice) LatestPriceForCurrencies(currencies ...string) *MaterialPrice {
	for _, currency := range currencies {
		currency = strings.ToUpper(strings.TrimSpace(currency))
		if currency == "" {
			continue
		}
		if price := m.LatestPrices[currency]; price != nil {
			return price
		}
		if m.LatestPrice != nil && strings.EqualFold(m.LatestPrice.Currency, currency) {
			return m.LatestPrice
		}
	}
	if len(m.LatestPrices) == 1 {
		for _, price := range m.LatestPrices {
			return price
		}
	}
	if len(m.LatestPrices) == 0 {
		return m.LatestPrice
	}
	return nil
}

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
	// CuttingCoefficient (Ф5а.2, 0270) is THE one visible, editable per-article dial that replaces
	// eight named losses nobody can measure separately: усадка, обход пороков, сращивание,
	// оттеночные полосы. Stored as a MULTIPLIER (1.0300 = +3%), not a percent. Invalid/NULL means
	// nobody has set one and the requirement path uses ×1 — so an unset article plans exactly as it
	// did before this field existed. There is deliberately no per-«класс ткани» default: that field
	// does not exist and inventing a taxonomy to feed defaults is the disease this replaces.
	CuttingCoefficient decimal.NullDecimal `db:"cutting_coefficient" valid:"-"`
	// CuttingCoefficientOmitted — поле ОТСУТСТВОВАЛО на проводе, а не «пришло пустым». Тот же приём,
	// что PurposeOmitted / IsSampleOmitted на строке спецификации (techcard.go), и по той же
	// причине: артикул сохраняется целиком, админка это SPA, и вкладка со старым бандлом — или
	// любой клиент из окна между деплоем бэка и деплоем фронта — этого поля не шлёт вовсе. Без
	// различения её сейв СТИРАЛ бы коэффициент, который выставил оператор: бесследно, потому что у
	// каталога нет ни дайджеста, ни журнала правок, а NULL неотличим от «никто не задавал».
	//
	// Признак НЕГАТИВНЫЙ намеренно: нулевое значение означает «писать как обычно», поэтому любой
	// внутренний конструктор (тесты, сидер, авто-создание бакета в output_variants) продолжает
	// работать как писал. Читается ТОЛЬКО на UPDATE — на INSERT «отсутствует» и «пусто» это одно и
	// то же NULL.
	CuttingCoefficientOmitted bool `db:"-" valid:"-"`
	// FabricThicknessMm (Ф4.8, 0283) is the thickness of ONE ply of the cloth, in millimetres — the
	// article's half of the предел стопки. The workshop owns the other half
	// (WorkshopSettings.MaxStackHeightCm) and the verdict is
	// Σ plies × FabricThicknessMm / 10 ≤ MaxStackHeightCm, computed on READ and never stored.
	//
	// INVALID (NULL) MEANS «НЕ ЗАМЕРЕНО», AND IT IS NOT ZERO. Zero would make every настил exactly
	// 0 cm tall and therefore comfortably within any limit — a confident verdict manufactured out of
	// missing data, which is the one outcome «нет толщины — нет проверки, не догадка» exists to
	// forbid. A reader must check .Valid and withhold the height entirely, not substitute a number.
	// There is deliberately no per-«класс ткани» default: that taxonomy does not exist, and inventing
	// one to feed a guess is exactly the disease CuttingCoefficient above already refused.
	FabricThicknessMm decimal.NullDecimal `db:"fabric_thickness_mm" valid:"-"`
	// FabricThicknessMmOmitted — поле ОТСУТСТВОВАЛО на проводе, а не «пришло пустым», word for word
	// the CuttingCoefficientOmitted rule one field up and for the identical reason: the article saves
	// whole, the admin is an SPA, and a tab holding an older bundle — or any client in the window
	// between the backend and the client deploy — does not send this field at all. Without the
	// distinction such a save would ERASE a number somebody took with calipers, and erase it without
	// a trace, because the catalogue carries neither a signed digest nor an edit journal.
	//
	// NEGATIVE on purpose, again: the zero value means «write as usual», so every internal
	// constructor (tests, seeder, auto-created buckets) keeps behaving as it always did. Read ONLY on
	// UPDATE — on INSERT «отсутствует» and «пусто» are the same NULL.
	FabricThicknessMmOmitted bool `db:"-" valid:"-"`
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

// EffectiveFabricWeightGsm resolves the ARTICLE's density (g/m²) with the same CTI-over-flat
// preference as EffectiveFabricWidthCm: typed material_fabric_attr.weight_gsm wins, the legacy flat
// material.fabric_weight_gsm is the fallback, an unset-or-zero typed value falls through.
//
// This is the ARTICLE's density and the only one with a consumer. Density also exists on the BOM
// LINE (tech_card_bom_item.fabric_weight_gsm) where nothing reads it — the two are NOT the same
// number and must not be folded together: the line's is a spec snapshot of what the card was drawn
// against, the article's is what the warehouse actually stocks and prices.
func (m *Material) EffectiveFabricWeightGsm() decimal.NullDecimal {
	if m.FabricAttr != nil && m.FabricAttr.WeightGsm.Valid && !m.FabricAttr.WeightGsm.Decimal.IsZero() {
		return m.FabricAttr.WeightGsm
	}
	return m.FabricWeightGsm
}

// EffectiveCuttingCoefficient is the article's cutting coefficient as a multiplier to apply, or
// invalid when the article has none (the caller must then multiply by nothing at all — NOT by a
// guessed default, which would silently inflate every existing plan). A stored value below 1 is
// treated as unset: a coefficient can only add to a norm, never shave it, and the DB CHECK already
// refuses one — this guards in-memory values that never went through it.
func (m *Material) EffectiveCuttingCoefficient() decimal.NullDecimal {
	if !m.CuttingCoefficient.Valid || m.CuttingCoefficient.Decimal.LessThan(decimal.NewFromInt(1)) {
		return decimal.NullDecimal{}
	}
	return m.CuttingCoefficient
}

// EffectiveFabricThicknessMm is the article's per-ply thickness in millimetres, or INVALID when the
// article has none — and an invalid answer must be propagated as «высота стопки неизвестна», never
// collapsed to a number. A stored value at or below zero is treated as unset for the same reason the
// coefficient treats < 1 as unset: the DB CHECK already refuses it, and this guards in-memory values
// that never passed through the column (a hand-built fixture, a future importer).
//
// Deliberately NOT a verdict helper. Whether a given настил fits is the lay path's business (it owns
// the ply count); this returns only the article's half, so the two halves of Ф4.8 cannot drift.
func (m *Material) EffectiveFabricThicknessMm() decimal.NullDecimal {
	if !m.FabricThicknessMm.Valid || m.FabricThicknessMm.Decimal.LessThanOrEqual(decimal.Zero) {
		return decimal.NullDecimal{}
	}
	return m.FabricThicknessMm
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

// fabricKgPerMetreDivisor folds the two unit conversions of the length→weight formula into one
// constant: cm→m (÷100) and g→kg (÷1000).
var fabricKgPerMetreDivisor = decimal.NewFromInt(100000)

// FabricLengthToKg converts a fabric length in METRES to KILOGRAMS for a roll of the given width and
// density: kg = metres × (widthCm ÷ 100) × gsm ÷ 1000.
//
// widthCm must be the FULL roll width, INCLUDING the кромка. The selvedge is bought and paid for and
// it physically weighs; billing by the cutting width (full − 2×selvedge) understates the weight the
// supplier invoices by 2–4%. This is the one place in the codebase that deliberately wants the full
// width rather than Material.UsableFabricWidthCm.
//
// Returns invalid when either input is missing or non-positive — a weight computed from a guessed
// width or density is a number nobody can defend, and an absent answer is the honest one.
func FabricLengthToKg(metres decimal.Decimal, fullWidthCm, gsm decimal.NullDecimal) decimal.NullDecimal {
	if !fullWidthCm.Valid || !gsm.Valid ||
		!fullWidthCm.Decimal.IsPositive() || !gsm.Decimal.IsPositive() {
		return decimal.NullDecimal{}
	}
	kg := metres.Mul(fullWidthCm.Decimal).Mul(gsm.Decimal).Div(fabricKgPerMetreDivisor)
	return decimal.NullDecimal{Decimal: kg, Valid: true}
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

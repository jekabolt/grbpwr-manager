package entity

import (
	"database/sql"
	"time"

	"github.com/shopspring/decimal"
)

type ColorwayNew struct {
	Product          *ColorwayInsert             `valid:"required"`
	SizeMeasurements []SizeWithMeasurementInsert `valid:"required"`
	MediaIds         []int                       `valid:"required"`
	Tags             []ColorwayTagInsert         `valid:"required"`
	Prices           []ColorwayPriceInsert       `valid:"required"` // At least one price required
}

type ColorwayFull struct {
	Product      *Colorway
	Sizes        []Variant
	Measurements []ProductMeasurement
	Media        []MediaFull
	Tags         []ColorwayTag
	Prices       []ColorwayPrice
}

// Category represents a hierarchical category structure
type Category struct {
	ID         int    `db:"category_id"`
	Name       string `db:"category_name"`
	LevelID    int    `db:"level_id"`
	Level      string `db:"level_name"`
	ParentID   *int   `db:"parent_id"`
	CountMen   int
	CountWomen int
}

// SizeSKUSystem is the controlled size family used to interpret an SKU ordinal. It is intentionally
// closed: adding a family changes the public SKU contract and therefore requires code, proto and DB
// migrations rather than another free-text value.
type SizeSKUSystem string

const (
	SizeSKUSystemApparel     SizeSKUSystem = "apparel"
	SizeSKUSystemShoe        SizeSKUSystem = "shoe"
	SizeSKUSystemCompositeTA SizeSKUSystem = "composite_ta"
	SizeSKUSystemCompositeBO SizeSKUSystem = "composite_bo"
)

// ValidSizeSKUSystems is the canonical set of size families, mirroring ValidSeasons/
// ValidProductTargetGenders below. It backs both IsValidSizeSKUSystem and the entity<->DB CHECK
// drift test (problem 033/50-F — internal/store/migrationlint) against migration 0147's
// chk_size_sku_contract.
var ValidSizeSKUSystems = map[SizeSKUSystem]bool{
	SizeSKUSystemApparel:     true,
	SizeSKUSystemShoe:        true,
	SizeSKUSystemCompositeTA: true,
	SizeSKUSystemCompositeBO: true,
}

func IsValidSizeSKUSystem(system SizeSKUSystem) bool {
	return ValidSizeSKUSystems[system]
}

// Size represents the size table.
type Size struct {
	Id         int           `db:"id"`
	Name       string        `db:"name"`
	SkuOrd     int           `db:"sku_ord"`    // required 1..99 ordinal for the variant SKU segment
	SkuSystem  SizeSKUSystem `db:"sku_system"` // controlled by SizeSKUSystem/DB CHECK
	CountMen   int
	CountWomen int
}

// Color is a controlled colour dictionary entry. Code is exactly 3 chars and unique; it feeds the
// colour segment of the SKU and is referenced by product.color_code and tech_card_colorway.color_code.
// Hex is the base shade; product.color_hex may override it per product.
type Color struct {
	ID         int            `db:"id"`
	Code       string         `db:"code"`
	Name       string         `db:"name"`
	Hex        sql.NullString `db:"hex"`
	ArchivedAt sql.NullTime   `db:"archived_at"`
}

// Collection represents a product collection with counts
type Collection struct {
	Name       string `db:"name"`
	CountMen   int
	CountWomen int
}

// MeasurementName represents the measurement_name table
type MeasurementName struct {
	Id   int    `db:"id"`
	Name string `db:"name"`
}

type GenderEnum string

const (
	Male   GenderEnum = "male"
	Female GenderEnum = "female"
	Unisex GenderEnum = "unisex"
)

// ColorwayStatus (the stored product.lifecycle_status) and its lifecycle state machine live in
// colorway_lifecycle.go. It is ORTHOGONAL to preorder, sold_out and hidden_for_non_qualified
// (availability window / derived stock / tier gating), which are not lifecycle states.

// ValidColorwayStatuses is the canonical set of STORABLE statuses, mirroring
// ValidSeasons/ValidProductTargetGenders. It backs the entity<->DB CHECK drift test
// (internal/store/migrationlint) against migration 0137's stored lifecycle_status
// `chk_product_lifecycle_status CHECK (BETWEEN 1 AND 4)`. UNKNOWN=0 is deliberately absent:
// it is never written (fail-closed read sentinel only).
var ValidColorwayStatuses = map[ColorwayStatus]bool{
	ColorwayStatusDraft:    true,
	ColorwayStatusActive:   true,
	ColorwayStatusHidden:   true,
	ColorwayStatusArchived: true,
}

func IsValidColorwayStatus(status ColorwayStatus) bool {
	return status.IsValid()
}

type SeasonEnum string

const (
	SeasonSS SeasonEnum = "SS" // Spring/Summer
	SeasonFW SeasonEnum = "FW" // Fall/Winter
	SeasonPF SeasonEnum = "PF" // Pre-Fall
	SeasonRC SeasonEnum = "RC" // Resort/Cruise
)

func (se SeasonEnum) String() string {
	switch se {
	case SeasonSS:
		return string(SeasonSS)
	case SeasonFW:
		return string(SeasonFW)
	case SeasonPF:
		return string(SeasonPF)
	case SeasonRC:
		return string(SeasonRC)
	default:
		return ""
	}
}

func IsValidSeason(s SeasonEnum) bool {
	_, ok := ValidSeasons[s]
	return ok
}

var ValidSeasons = map[SeasonEnum]bool{
	SeasonSS: true,
	SeasonFW: true,
	SeasonPF: true,
	SeasonRC: true,
}

func (ge GenderEnum) String() string {
	switch ge {
	case Male:
		return string(Male)
	case Female:
		return string(Female)
	case Unisex:
		return string(Unisex)
	default:
		return string(Unisex)
	}
}

func IsValidTargetGender(g GenderEnum) bool {
	_, ok := ValidProductTargetGenders[g]
	return ok
}

// ValidMeasurementNames is a map containing all the valid measurement names.
var ValidProductTargetGenders = map[GenderEnum]bool{
	Male:   true,
	Female: true,
	Unisex: true,
}

type ColorwayBodyInsert struct {
	Preorder           sql.NullTime        `db:"preorder" valid:"-"`
	Brand              string              `db:"brand" valid:"required"`
	Color              string              `db:"color" valid:"required"` // resolved dictionary name; never accepted from API
	ColorCode          string              `db:"color_code" valid:"required"`
	ColorHexOverride   sql.NullString      `db:"color_hex" valid:"-"`
	CountryOfOrigin    string              `db:"country_of_origin" valid:"required"`
	SalePercentage     decimal.NullDecimal `db:"sale_percentage" valid:"-"`
	TopCategoryId      int                 `db:"top_category_id" valid:"required"`
	SubCategoryId      sql.NullInt32       `db:"sub_category_id" valid:"-"`
	TypeId             sql.NullInt32       `db:"type_id" valid:"-"`
	ModelWearsHeightCm sql.NullInt32       `db:"model_wears_height_cm" valid:"-"`
	ModelWearsSizeId   sql.NullInt32       `db:"model_wears_size_id" valid:"-"`
	CareInstructions   sql.NullString      `db:"care_instructions" valid:"-"`
	Composition        sql.NullString      `db:"composition" valid:"-"`
	TargetGender       GenderEnum          `db:"target_gender"`
	Season             SeasonEnum          `db:"season" valid:"required"`
	Collection         string              `db:"collection" valid:"-"`
	Fit                sql.NullString      `db:"fit" valid:"-"`
	// MinTier is the minimum loyalty tier code (0/1/2/99) required to purchase.
	MinTier int16 `db:"min_tier" valid:"-"`
	// HiddenForNonQualified hides the product entirely from non-qualified tiers.
	HiddenForNonQualified bool `db:"hidden_for_non_qualified" valid:"-"`
	// CompositionEntries is the style's structured fibre composition (S17/M1 fix), resolved
	// server-side from style_composition alongside the legacy free-text Composition above — never
	// instead of it (M1: composition used to be silently overloaded with a JSON encoding of this same
	// data once style_composition gained rows; that overload is removed, this typed field is the
	// replacement). Populated in Go from the JSON aggregate query.go's styleCompositionEntriesSelect
	// produces (db:"-": not a plain scanned column, unmarshalled by the caller).
	CompositionEntries []CompositionEntry `db:"-" valid:"-"`
}

// ColorwayPrice represents a product price in a specific currency
type ColorwayPrice struct {
	Id        int             `db:"id"`
	ProductId int             `db:"product_id"`
	Currency  string          `db:"currency"`
	Price     decimal.Decimal `db:"price"`
	CreatedAt time.Time       `db:"created_at"`
	UpdatedAt time.Time       `db:"updated_at"`
}

// ColorwayPriceInsert for inserting/updating product prices
type ColorwayPriceInsert struct {
	Currency string          `db:"currency" valid:"required,length(3|4)"` // ISO 4217 (3) or USDT (4)
	Price    decimal.Decimal `db:"price" valid:"required"`
}

type ColorwayBody struct {
	ProductBodyInsert ColorwayBodyInsert          `valid:"required"`
	Translations      []ColorwayTranslationInsert `valid:"required"`
}

// StylePatch is the set of catalogue-style facts written ONLY by UpdateStyle (R4/§14.7): the garment
// facts invariant across a style's colourways. It mirrors the style-owned subset of ColorwayBodyInsert
// and drives the shared styleFieldsSet SQL. category_id stays a PLM/UpdateTechCard fact and is not here.
type StylePatch struct {
	Brand              string
	Season             SeasonEnum
	Collection         string
	TargetGender       GenderEnum
	Fit                sql.NullString
	Composition        sql.NullString
	CareInstructions   sql.NullString
	ModelWearsHeightCm sql.NullInt32
	ModelWearsSizeId   sql.NullInt32
	TopCategoryId      int
	SubCategoryId      sql.NullInt32
	TypeId             sql.NullInt32
}

func (pb *ColorwayBody) SalePercentageDecimal() decimal.Decimal {
	if pb.ProductBodyInsert.SalePercentage.Valid {
		return pb.ProductBodyInsert.SalePercentage.Decimal.Round(2)
	}
	return decimal.Zero
}

// Colorway represents the product table
type Colorway struct {
	Id              int             `db:"id"`
	CreatedAt       time.Time       `db:"created_at"`
	UpdatedAt       time.Time       `db:"updated_at"`
	DeletedAt       sql.NullTime    `db:"deleted_at"`
	Slug            string          `db:"slug"`
	SKU             string          `db:"sku"`
	SkuLockedAt     sql.NullTime    `db:"sku_locked_at"` // freeze marker; non-NULL => SKU never rebuilt (first sale/label)
	PublishedAt     sql.NullTime    `db:"published_at"`  // R6: audit of first publish (lifecycle.go COALESCE(published_at, NOW()))
	ProductDisplay  ColorwayDisplay `valid:"required"`
	Prices          []ColorwayPrice // Multi-currency prices
	SoldOut         bool            // Indicates if product is sold out (all sizes have quantity <= 0)
	LifecycleStatus ColorwayStatus  `db:"lifecycle_status"` // stored lifecycle: draft/active/hidden/archived (R6)
	StyleId         int             `db:"style_id"`         // FK tech_card: every product (colourway) belongs to a style (PR6 P1)
}

// IsPubliclyVisible reports whether the product is exposed on the storefront: only ACTIVE colourways
// are (draft/hidden/archived are not). It is the single Go predicate behind the storefront read filter
// (lifecycle_status = 2); tier gating (HiddenForNonQualified) and stock (SoldOut) are separate axes
// applied on top of it, not part of this decision.
func (p *Colorway) IsPubliclyVisible() bool {
	return p.LifecycleStatus == ColorwayStatusActive
}

// MinTier is the minimum loyalty tier code (0/1/2/99) required to PURCHASE this colourway.
// It gates purchase and drives the storefront `locked` teaser flag; it does NOT by itself hide
// the row (HiddenForNonQualified does that). Convenience accessor over the nested body field.
func (p *Colorway) MinTier() int16 {
	return p.ProductDisplay.ProductBody.ProductBodyInsert.MinTier
}

// HiddenForNonQualified reports whether this colourway is hidden ENTIRELY from viewers who do not
// qualify for its MinTier (as opposed to being shown as a locked teaser). FALSE (default) => show
// as a locked teaser to everyone; TRUE => never reveal to a non-qualifying viewer anywhere.
// Convenience accessor over the nested body field.
func (p *Colorway) HiddenForNonQualified() bool {
	return p.ProductDisplay.ProductBody.ProductBodyInsert.HiddenForNonQualified
}

type ColorwayInsert struct {
	ProductBodyInsert ColorwayBodyInsert `valid:"required"`
	// ThumbnailMediaID is optional at write time: a DRAFT colourway may carry no thumbnail yet (0 => SQL
	// NULL, product.thumbnail_id nullable since 0151). A thumbnail is required only to go ACTIVE, enforced
	// on the →ACTIVE lifecycle edge (checkColorwayHasThumbnail), not by struct validation here.
	ThumbnailMediaID          int                         `db:"thumbnail_media_id" valid:"-"`
	SecondaryThumbnailMediaID sql.NullInt32               `db:"secondary_thumbnail_media_id" valid:"-"`
	Translations              []ColorwayTranslationInsert `valid:"required"`
	Prices                    []ColorwayPriceInsert       `valid:"required"` // At least one price required
	// CostPrice is the confidential per-unit cost of goods (COGS) in base currency
	// (EUR), used for margin analytics. Invalid/NULL leaves the stored value unchanged
	// on update. Never serialized on the storefront read path — write-only.
	CostPrice decimal.NullDecimal `db:"cost_price" valid:"-"`
}

type ColorwayDisplay struct {
	ProductBody        ColorwayBody `valid:"required"`
	Thumbnail          MediaFull    `valid:"required"`
	SecondaryThumbnail *MediaFull   `valid:"-"`
}

// ColorwayCostInfo carries the confidential COGS fields of a product for the admin surface
// only (never the storefront). Source is "manual" | "tech_card" | "" (unset); the tech-card
// ids are 0 when absent.
type ColorwayCostInfo struct {
	CostPrice           decimal.NullDecimal `db:"cost_price"`
	CostPriceSource     sql.NullString      `db:"cost_price_source"`
	CostPriceTechCardID sql.NullInt32       `db:"cost_price_tech_card_id"`
	CostPriceUpdatedAt  sql.NullTime        `db:"cost_price_updated_at"`
	PrimaryTechCardID   sql.NullInt32       `db:"primary_tech_card_id"`
}

// ColorwayCustoms carries the per-product customs data used to build an international shipping
// label's customs declaration (Sendcloud). HSCode / CustomsDescription are the optional customs-only
// fields (a product without them ships domestically/intra-EU fine). CountryOfOrigin is the existing
// required core product field (free-text manufacture country) surfaced here read-only — it is set
// via the product form, and resolved to ISO-2 at label build time; SetProductCustoms never writes it.
type ColorwayCustoms struct {
	HSCode             sql.NullString `db:"hs_code"`
	CountryOfOrigin    sql.NullString `db:"country_of_origin"`
	CustomsDescription sql.NullString `db:"customs_description"`
}

type ProductMeasurementUpdate struct {
	SizeId            int             `db:"size_id"`
	MeasurementNameId int             `db:"measurement_name_id"`
	MeasurementValue  decimal.Decimal `db:"measurement_value"`
}

type SizeWithMeasurementInsert struct {
	ProductSize  VariantInsert
	Measurements []ProductMeasurementInsert
}

type SizeWithMeasurement struct {
	ProductSize  Variant
	Measurements []ProductMeasurement
}

// ProductSizes represents the product_size table
type Variant struct {
	Id        int             `db:"id"`
	Quantity  decimal.Decimal `db:"quantity"`
	ProductId int             `db:"product_id"`
	SizeId    int             `db:"size_id"`
	SKU       sql.NullString  `db:"sku"`    // first-class variant SKU (SS26-00021-BLK-04)
	Status    uint8           `db:"status"` // VariantStatus: 1=active, 2=archived (R2, migration 0155)
}

func (ps *Variant) QuantityDecimal() decimal.Decimal {
	return ps.Quantity.Round(0)
}

// SoldOutFromSizes is the single Go definition of the derived sold_out flag: a product is sold out
// when the total available quantity across its sizes is <= 0 (which includes having no sizes). The
// SQL read path computes the same thing server-side via one shared expression (store/product's
// soldOutSelect / productStockExpr); keep the two in agreement (PR5-B).
func SoldOutFromSizes(sizes []Variant) bool {
	total := decimal.Zero
	for i := range sizes {
		total = total.Add(sizes[i].Quantity)
	}
	return total.LessThanOrEqual(decimal.Zero)
}

// ProductSizes for insert represents the product_size table
type VariantInsert struct {
	Quantity decimal.Decimal `db:"quantity"`
	SizeId   int             `db:"size_id"`
}

func (psi *VariantInsert) QuantityDecimal() decimal.Decimal {
	return psi.Quantity.Round(0)
}

// ProductMeasurement is the per-colourway view of a size-chart entry, reconstructed from the
// style-level tech_card_size_measurement (PR6 P3) joined to the colourway's product_size.
type ProductMeasurement struct {
	Id                int             `db:"id"`
	ProductId         int             `db:"product_id"`
	ProductSizeId     int             `db:"product_size_id"`
	MeasurementNameId int             `db:"measurement_name_id"`
	MeasurementValue  decimal.Decimal `db:"measurement_value"`
}

// ProductMeasurementInsert is one measurement value in a size-chart write (persisted to the
// style-level tech_card_size_measurement, PR6 P3).
type ProductMeasurementInsert struct {
	MeasurementNameId int             `db:"measurement_name_id"`
	MeasurementValue  decimal.Decimal `db:"measurement_value"`
}

// ColorwayTag represents the product_tag table
type ColorwayTag struct {
	Id        int `db:"id"`
	ProductId int `db:"product_id"`
	ColorwayTagInsert
}

// ProductTag represents the product_tag table
type ColorwayTagInsert struct {
	Tag string `db:"tag"`
}

// ColorwayTranslation represents the product_translation table
type ColorwayTranslation struct {
	Id          int       `db:"id"`
	ProductId   int       `db:"product_id"`
	LanguageId  int       `db:"language_id"`
	Name        string    `db:"name"`
	Description string    `db:"description"`
	CreatedAt   time.Time `db:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"`
}

// ColorwayTranslationInsert represents the product_translation table for insert operations
type ColorwayTranslationInsert struct {
	LanguageId  int    `db:"language_id" json:"language_id" valid:"required"`
	Name        string `db:"name" json:"name" valid:"required"`
	Description string `db:"description" json:"description" valid:"required"`
}

// StockChangeSource represents the source of a product stock change.
type StockChangeSource string

const (
	StockChangeSourceAdminNewProduct  StockChangeSource = "admin_new_product"
	StockChangeSourceManualAdjustment StockChangeSource = "manual_adjustment"
	StockChangeSourceOrderPaid        StockChangeSource = "order_paid"
	StockChangeSourceOrderCustom      StockChangeSource = "order_custom"
	StockChangeSourceOrderReturned    StockChangeSource = "order_returned"
	StockChangeSourceOrderCancelled   StockChangeSource = "order_cancelled"
	// StockChangeSourceProductionReceived is stock added by receiving a production run (task 09).
	StockChangeSourceProductionReceived StockChangeSource = "production_received"
)

// StockChangeReason represents the reason for a stock change.
type StockChangeReason string

const (
	// admin_new_product reasons
	StockChangeReasonInitialStock StockChangeReason = "initial_stock"
	// manual_adjustment reasons
	StockChangeReasonStockCount      StockChangeReason = "stock_count"
	StockChangeReasonDamage          StockChangeReason = "damage"
	StockChangeReasonLoss            StockChangeReason = "loss"
	StockChangeReasonFound           StockChangeReason = "found"
	StockChangeReasonCorrection      StockChangeReason = "correction"
	StockChangeReasonReservedRelease StockChangeReason = "reserved_release"
	StockChangeReasonOther           StockChangeReason = "other"
	// order_reserved reasons
	StockChangeReasonOrder StockChangeReason = "order"
	// order_custom_reserved reasons
	StockChangeReasonCustomOrder StockChangeReason = "custom_order"
	// order_returned reasons
	StockChangeReasonReturnToStock StockChangeReason = "return_to_stock"
	// order_cancelled reasons
	StockChangeReasonOrderCancelled StockChangeReason = "order_cancelled"
)

// ValidReasonsForSource maps each source to its allowed reasons.
var ValidReasonsForSource = map[StockChangeSource][]StockChangeReason{
	StockChangeSourceAdminNewProduct:  {StockChangeReasonInitialStock},
	StockChangeSourceManualAdjustment: {StockChangeReasonStockCount, StockChangeReasonDamage, StockChangeReasonLoss, StockChangeReasonFound, StockChangeReasonCorrection, StockChangeReasonReservedRelease, StockChangeReasonOther},
	StockChangeSourceOrderPaid:        {StockChangeReasonOrder},
	StockChangeSourceOrderCustom:      {StockChangeReasonCustomOrder},
	StockChangeSourceOrderReturned:    {StockChangeReasonReturnToStock},
	StockChangeSourceOrderCancelled:   {StockChangeReasonOrderCancelled},
}

// StockChangeSignPositive means the source only allows positive deltas.
// StockChangeSignNegative means the source only allows negative deltas.
// StockChangeSignBoth means the source allows both.
type StockChangeSign int

const (
	StockChangeSignPositive StockChangeSign = iota
	StockChangeSignNegative
	StockChangeSignBoth
)

// AllowedSignForSource maps each source to its allowed sign direction.
var AllowedSignForSource = map[StockChangeSource]StockChangeSign{
	StockChangeSourceAdminNewProduct:  StockChangeSignPositive,
	StockChangeSourceManualAdjustment: StockChangeSignBoth,
	StockChangeSourceOrderPaid:        StockChangeSignNegative,
	StockChangeSourceOrderCustom:      StockChangeSignNegative,
	StockChangeSourceOrderReturned:    StockChangeSignPositive,
	StockChangeSourceOrderCancelled:   StockChangeSignPositive,
}

// IsValidReasonForSource checks if a reason is valid for a given source.
func IsValidReasonForSource(source StockChangeSource, reason StockChangeReason) bool {
	reasons, ok := ValidReasonsForSource[source]
	if !ok {
		return false
	}
	for _, r := range reasons {
		if r == reason {
			return true
		}
	}
	return false
}

// StockAdjustmentMode represents the mode of stock adjustment.
type StockAdjustmentMode string

const (
	StockAdjustmentModeSet    StockAdjustmentMode = "set"
	StockAdjustmentModeAdjust StockAdjustmentMode = "adjust"
)

// StockAdjustmentDirection represents the direction of stock adjustment.
type StockAdjustmentDirection string

const (
	StockAdjustmentDirectionIncrease StockAdjustmentDirection = "increase"
	StockAdjustmentDirectionDecrease StockAdjustmentDirection = "decrease"
)

// StockUpdateMode selects how UpdateProductSizeStockWithHistory interprets its amount argument. Set
// treats amount as the absolute final quantity; Adjust treats it as a signed delta applied to the
// row-locked current quantity, so concurrent adjustments compose instead of clobbering each other
// (problem 025).
type StockUpdateMode int

const (
	StockUpdateModeSet    StockUpdateMode = iota // amount is the absolute final quantity
	StockUpdateModeAdjust                        // amount is a signed delta on the current quantity
)

// StockChangeInsert represents a row to insert into product_stock_change_history.
type StockChangeInsert struct {
	ProductId           sql.NullInt32       `db:"product_id"`
	SizeId              sql.NullInt32       `db:"size_id"`
	QuantityDelta       decimal.Decimal     `db:"quantity_delta"`
	QuantityBefore      decimal.Decimal     `db:"quantity_before"`
	QuantityAfter       decimal.Decimal     `db:"quantity_after"`
	Source              string              `db:"source"`
	OrderId             sql.NullInt32       `db:"order_id"`
	OrderUUID           sql.NullString      `db:"order_uuid"`
	AdminUsername       sql.NullString      `db:"admin_username"`
	ReferenceId         sql.NullString      `db:"reference_id"`
	Reason              sql.NullString      `db:"reason"`
	Comment             sql.NullString      `db:"comment"`
	OrderComment        sql.NullString      `db:"order_comment"`
	PriceBeforeDiscount decimal.NullDecimal `db:"price_before_discount"`
	DiscountAmount      decimal.NullDecimal `db:"discount_amount"`
	PaidCurrency        sql.NullString      `db:"paid_currency"`
	PaidAmount          decimal.NullDecimal `db:"paid_amount"`
	PayoutBaseAmount    decimal.NullDecimal `db:"payout_base_amount"`
	PayoutBaseCurrency  sql.NullString      `db:"payout_base_currency"`
}

// StockChange represents a row from product_stock_change_history.
type StockChange struct {
	Id                  int             `db:"id"`
	ProductId           int             `db:"product_id"`
	SizeId              int             `db:"size_id"`
	QuantityDelta       decimal.Decimal `db:"quantity_delta"`
	QuantityBefore      decimal.Decimal `db:"quantity_before"`
	QuantityAfter       decimal.Decimal `db:"quantity_after"`
	Source              string          `db:"source"`
	OrderId             int             `db:"order_id"`
	OrderUUID           string          `db:"order_uuid"`
	AdminUsername       string          `db:"admin_username"`
	ReferenceId         string          `db:"reference_id"`
	Reason              string          `db:"reason"`
	Comment             string          `db:"comment"`
	OrderComment        string          `db:"order_comment"`
	PriceBeforeDiscount string          `db:"price_before_discount"`
	DiscountAmount      string          `db:"discount_amount"`
	PaidCurrency        string          `db:"paid_currency"`
	PaidAmount          string          `db:"paid_amount"`
	PayoutBaseAmount    string          `db:"payout_base_amount"`
	PayoutBaseCurrency  string          `db:"payout_base_currency"`
	CreatedAt           time.Time       `db:"created_at"`
}

// StockHistoryParams is passed when recording stock changes from order-related flows.
type StockHistoryParams struct {
	Source           StockChangeSource
	OrderId          int
	OrderUUID        string
	OrderCurrency    string
	OrderComment     string          // order comment from customer
	PromoDiscount    decimal.Decimal // promo code discount percentage (0-100)
	PayoutBaseAmount decimal.Decimal // total payout in base currency (EUR)
}

// StockChangeRow represents a simplified stock change for API responses.
type StockChangeRow struct {
	Date                time.Time       `db:"created_at"`
	SKU                 string          `db:"sku"`
	SizeName            string          `db:"size_name"`
	AmountChanged       decimal.Decimal `db:"quantity_delta"`
	RemainingStock      decimal.Decimal `db:"quantity_after"`
	Source              string          `db:"source"`
	ReferenceId         string          `db:"reference_id"`
	OrderUUID           string          `db:"order_uuid"`
	AdminUsername       string          `db:"admin_username"`
	Reason              string          `db:"reason"`
	Comment             string          `db:"comment"`
	OrderComment        string          `db:"order_comment"`
	PriceBeforeDiscount string          `db:"price_before_discount"`
	DiscountAmount      string          `db:"discount_amount"`
	PaidCurrency        string          `db:"paid_currency"`
	PaidAmount          string          `db:"paid_amount"`
	PayoutBaseAmount    string          `db:"payout_base_amount"`
	PayoutBaseCurrency  string          `db:"payout_base_currency"`
}

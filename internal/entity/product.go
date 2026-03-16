package entity

import (
	"database/sql"
	"time"

	"github.com/shopspring/decimal"
)

type ProductNew struct {
	Product          *ProductInsert              `valid:"required"`
	SizeMeasurements []SizeWithMeasurementInsert `valid:"required"`
	MediaIds         []int                       `valid:"required"`
	Tags             []ProductTagInsert          `valid:"required"`
	Prices           []ProductPriceInsert        `valid:"required"` // At least one price required
}

type ProductFull struct {
	Product      *Product
	Sizes        []ProductSize
	Measurements []ProductMeasurement
	Media        []MediaFull
	Tags         []ProductTag
	Prices       []ProductPrice
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

// Size represents the size table
type Size struct {
	Id         int    `db:"id"`
	Name       string `db:"name"`
	CountMen   int
	CountWomen int
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

type ProductBodyInsert struct {
	Preorder           sql.NullTime        `db:"preorder" valid:"-"`
	Brand              string              `db:"brand" valid:"required"`
	Color              string              `db:"color" valid:"required"`
	ColorHex           string              `db:"color_hex" valid:"required,hexcolor"`
	CountryOfOrigin    string              `db:"country_of_origin" valid:"required"`
	SalePercentage     decimal.NullDecimal `db:"sale_percentage" valid:"-"`
	TopCategoryId      int                 `db:"top_category_id" valid:"required"`
	SubCategoryId      sql.NullInt32       `db:"sub_category_id" valid:"-"`
	TypeId             sql.NullInt32       `db:"type_id" valid:"-"`
	ModelWearsHeightCm sql.NullInt32       `db:"model_wears_height_cm" valid:"-"`
	ModelWearsSizeId   sql.NullInt32       `db:"model_wears_size_id" valid:"-"`
	CareInstructions   sql.NullString      `db:"care_instructions" valid:"-"`
	Composition        sql.NullString      `db:"composition" valid:"-"`
	Hidden             sql.NullBool        `db:"hidden" valid:"-"`
	TargetGender       GenderEnum          `db:"target_gender"`
	Season             SeasonEnum          `db:"season" valid:"required"`
	Version            string              `db:"version" valid:"-"`
	Collection         string              `db:"collection" valid:"-"`
	Fit                sql.NullString      `db:"fit" valid:"-"`
}

// ProductPrice represents a product price in a specific currency
type ProductPrice struct {
	Id        int             `db:"id"`
	ProductId int             `db:"product_id"`
	Currency  string          `db:"currency"`
	Price     decimal.Decimal `db:"price"`
	CreatedAt time.Time       `db:"created_at"`
	UpdatedAt time.Time       `db:"updated_at"`
}

// ProductPriceInsert for inserting/updating product prices
type ProductPriceInsert struct {
	Currency string          `db:"currency" valid:"required,length(3|3)"`
	Price    decimal.Decimal `db:"price" valid:"required"`
}

type ProductBody struct {
	ProductBodyInsert ProductBodyInsert          `valid:"required"`
	Translations      []ProductTranslationInsert `valid:"required"`
}

func (pb *ProductBody) SalePercentageDecimal() decimal.Decimal {
	if pb.ProductBodyInsert.SalePercentage.Valid {
		return pb.ProductBodyInsert.SalePercentage.Decimal.Round(2)
	}
	return decimal.Zero
}

// Product represents the product table
type Product struct {
	Id             int            `db:"id"`
	CreatedAt      time.Time      `db:"created_at"`
	UpdatedAt      time.Time      `db:"updated_at"`
	DeletedAt      sql.NullTime   `db:"deleted_at"`
	Slug           string         `db:"slug"`
	SKU            string         `db:"sku"`
	ProductDisplay ProductDisplay `valid:"required"`
	Prices         []ProductPrice // Multi-currency prices
	SoldOut        bool           // Indicates if product is sold out (all sizes have quantity <= 0)
}

type ProductInsert struct {
	ProductBodyInsert         ProductBodyInsert          `valid:"required"`
	ThumbnailMediaID          int                        `db:"thumbnail_media_id" valid:"required"`
	SecondaryThumbnailMediaID sql.NullInt32              `db:"secondary_thumbnail_media_id" valid:"-"`
	Translations              []ProductTranslationInsert `valid:"required"`
	Prices                    []ProductPriceInsert       `valid:"required"` // At least one price required
}

type ProductDisplay struct {
	ProductBody        ProductBody `valid:"required"`
	Thumbnail          MediaFull   `valid:"required"`
	SecondaryThumbnail *MediaFull  `valid:"-"`
}

type ProductMeasurementUpdate struct {
	SizeId            int             `db:"size_id"`
	MeasurementNameId int             `db:"measurement_name_id"`
	MeasurementValue  decimal.Decimal `db:"measurement_value"`
}

type SizeWithMeasurementInsert struct {
	ProductSize  ProductSizeInsert
	Measurements []ProductMeasurementInsert
}

type SizeWithMeasurement struct {
	ProductSize  ProductSize
	Measurements []ProductMeasurement
}

// ProductSizes represents the product_size table
type ProductSize struct {
	Id        int             `db:"id"`
	Quantity  decimal.Decimal `db:"quantity"`
	ProductId int             `db:"product_id"`
	SizeId    int             `db:"size_id"`
}

func (ps *ProductSize) QuantityDecimal() decimal.Decimal {
	return ps.Quantity.Round(0)
}

// ProductSizes for insert represents the product_size table
type ProductSizeInsert struct {
	Quantity decimal.Decimal `db:"quantity"`
	SizeId   int             `db:"size_id"`
}

func (psi *ProductSizeInsert) QuantityDecimal() decimal.Decimal {
	return psi.Quantity.Round(0)
}

// SizeMeasurement represents the size_measurement table
type ProductMeasurement struct {
	Id                int             `db:"id"`
	ProductId         int             `db:"product_id"`
	ProductSizeId     int             `db:"product_size_id"`
	MeasurementNameId int             `db:"measurement_name_id"`
	MeasurementValue  decimal.Decimal `db:"measurement_value"`
}

// SizeMeasurement represents the size_measurement table
type ProductMeasurementInsert struct {
	MeasurementNameId int             `db:"measurement_name_id"`
	MeasurementValue  decimal.Decimal `db:"measurement_value"`
}

// ProductTag represents the product_tag table
type ProductTag struct {
	Id        int `db:"id"`
	ProductId int `db:"product_id"`
	ProductTagInsert
}

// ProductTag represents the product_tag table
type ProductTagInsert struct {
	Tag string `db:"tag"`
}

// ProductTranslation represents the product_translation table
type ProductTranslation struct {
	Id          int       `db:"id"`
	ProductId   int       `db:"product_id"`
	LanguageId  int       `db:"language_id"`
	Name        string    `db:"name"`
	Description string    `db:"description"`
	CreatedAt   time.Time `db:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"`
}

// ProductTranslationInsert represents the product_translation table for insert operations
type ProductTranslationInsert struct {
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

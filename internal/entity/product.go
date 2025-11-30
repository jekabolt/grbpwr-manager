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

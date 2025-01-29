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
}

type ProductFull struct {
	Product      *Product
	Sizes        []ProductSize
	Measurements []ProductMeasurement
	Media        []MediaFull
	Tags         []ProductTag
}

// Category represents a hierarchical category structure
type Category struct {
	ID       int    `db:"category_id"`
	Name     string `db:"category_name"`
	LevelID  int    `db:"level_id"`
	Level    string `db:"level_name"`
	ParentID *int   `db:"parent_id"`
}

// Size represents the size table
type Size struct {
	Id   int    `db:"id"`
	Name string `db:"name"`
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

type ProductBody struct {
	Preorder           sql.NullTime        `db:"preorder" valid:"-"`
	Name               string              `db:"name" valid:"required"`
	Brand              string              `db:"brand" valid:"required"`
	SKU                string              `db:"sku" valid:"required,alphanum"`
	Color              string              `db:"color" valid:"required"`
	ColorHex           string              `db:"color_hex" valid:"required,hexcolor"`
	CountryOfOrigin    string              `db:"country_of_origin" valid:"required"`
	Price              decimal.Decimal     `db:"price" valid:"required"`
	SalePercentage     decimal.NullDecimal `db:"sale_percentage" valid:"-"`
	TopCategoryId      int                 `db:"top_category_id" valid:"required"`
	SubCategoryId      sql.NullInt32       `db:"sub_category_id" valid:"-"`
	TypeId             sql.NullInt32       `db:"type_id" valid:"-"`
	ModelWearsHeightCm sql.NullInt32       `db:"model_wears_height_cm" valid:"-"`
	ModelWearsSizeId   sql.NullInt32       `db:"model_wears_size_id" valid:"-"`
	Description        string              `db:"description" valid:"required"`
	Hidden             sql.NullBool        `db:"hidden" valid:"-"`
	TargetGender       GenderEnum          `db:"target_gender"`
	CareInstructions   sql.NullString      `db:"care_instructions" valid:"-"`
	Composition        sql.NullString      `db:"composition" valid:"-"`
}

func (pb *ProductBody) PriceDecimal() decimal.Decimal {
	return pb.Price.Round(2)
}

func (pb *ProductBody) SalePercentageDecimal() decimal.Decimal {
	if pb.SalePercentage.Valid {
		return pb.SalePercentage.Decimal.Round(2)
	}
	return decimal.Zero
}

// Product represents the product table
type Product struct {
	Id        int       `db:"id"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
	ProductDisplay
}

type ProductInsert struct {
	ProductBody
	ThumbnailMediaID int `db:"thumbnail_id"`
}

type ProductDisplay struct {
	ProductBody
	MediaFull
	ThumbnailMediaID int `db:"thumbnail_id"`
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

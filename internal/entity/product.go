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

type CategoryEnum string

const (
	TShirt    CategoryEnum = "t-shirt"
	Jeans     CategoryEnum = "jeans"
	Dress     CategoryEnum = "dress"
	Jacket    CategoryEnum = "jacket"
	Sweater   CategoryEnum = "sweater"
	Pant      CategoryEnum = "pant"
	Skirt     CategoryEnum = "skirt"
	Short     CategoryEnum = "short"
	Blazer    CategoryEnum = "blazer"
	Coat      CategoryEnum = "coat"
	Socks     CategoryEnum = "socks"
	Underwear CategoryEnum = "underwear"
	Bra       CategoryEnum = "bra"
	Hat       CategoryEnum = "hat"
	Scarf     CategoryEnum = "scarf"
	Gloves    CategoryEnum = "gloves"
	Shoes     CategoryEnum = "shoes"
	Belt      CategoryEnum = "belt"
	Bag       CategoryEnum = "bag"
	Other     CategoryEnum = "other"
)

// ValidCategories is a map containing all valid categories.
var ValidCategories = map[CategoryEnum]bool{
	TShirt:    true,
	Jeans:     true,
	Dress:     true,
	Jacket:    true,
	Sweater:   true,
	Pant:      true,
	Skirt:     true,
	Short:     true,
	Blazer:    true,
	Coat:      true,
	Socks:     true,
	Underwear: true,
	Bra:       true,
	Hat:       true,
	Scarf:     true,
	Gloves:    true,
	Shoes:     true,
	Belt:      true,
	Bag:       true,
	Other:     true,
}

// Category represents the category table
type Category struct {
	Id   int          `db:"id"`
	Name CategoryEnum `db:"name"`
}

type SizeEnum string

const (
	XXS SizeEnum = "xxs"
	XS  SizeEnum = "xs"
	S   SizeEnum = "s"
	M   SizeEnum = "m"
	L   SizeEnum = "l"
	XL  SizeEnum = "xl"
	XXL SizeEnum = "xxl"
	OS  SizeEnum = "os"
)

// ValidSizes is a map containing all the valid sizes.
var ValidSizes = map[SizeEnum]bool{
	XXS: true,
	XS:  true,
	S:   true,
	M:   true,
	L:   true,
	XL:  true,
	XXL: true,
	OS:  true,
}

// Size represents the size table
type Size struct {
	Id   int      `db:"id"`
	Name SizeEnum `db:"name"`
}

type MeasurementNameEnum string

const (
	Waist     MeasurementNameEnum = "waist"
	Inseam    MeasurementNameEnum = "inseam"
	Length    MeasurementNameEnum = "length"
	Rise      MeasurementNameEnum = "rise"
	Hips      MeasurementNameEnum = "hips"
	Shoulders MeasurementNameEnum = "shoulders"
	Bust      MeasurementNameEnum = "bust"
	Sleeve    MeasurementNameEnum = "sleeve"
	Width     MeasurementNameEnum = "width"
	Height    MeasurementNameEnum = "height"
)

// ValidMeasurementNames is a map containing all the valid measurement names.
var ValidMeasurementNames = map[MeasurementNameEnum]bool{
	Waist:     true,
	Inseam:    true,
	Length:    true,
	Rise:      true,
	Hips:      true,
	Shoulders: true,
	Bust:      true,
	Sleeve:    true,
	Width:     true,
	Height:    true,
}

// MeasurementName represents the measurement_name table
type MeasurementName struct {
	Id   int                 `db:"id"`
	Name MeasurementNameEnum `db:"name"`
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
	Preorder         sql.NullTime        `db:"preorder" valid:"-"`
	Name             string              `db:"name" valid:"required"`
	Brand            string              `db:"brand" valid:"required"`
	SKU              string              `db:"sku" valid:"required,alphanum"`
	Color            string              `db:"color" valid:"required"`
	ColorHex         string              `db:"color_hex" valid:"required,hexcolor"`
	CountryOfOrigin  string              `db:"country_of_origin" valid:"required"`
	Price            decimal.Decimal     `db:"price" valid:"required"`
	SalePercentage   decimal.NullDecimal `db:"sale_percentage" valid:"-"`
	CategoryId       int                 `db:"category_id" valid:"required"`
	Description      string              `db:"description" valid:"required"`
	Hidden           sql.NullBool        `db:"hidden" valid:"-"`
	TargetGender     GenderEnum          `db:"target_gender"`
	CareInstructions sql.NullString      `db:"care_instructions" valid:"-"`
	Composition      sql.NullString      `db:"composition" valid:"-"`
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

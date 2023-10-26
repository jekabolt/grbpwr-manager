package entity

import (
	"database/sql"
	"time"

	"github.com/shopspring/decimal"
)

type ProductNew struct {
	Product          *ProductInsert
	SizeMeasurements []SizeWithMeasurementInsert
	Media            []ProductMediaInsert
	Tags             []ProductTagInsert
}

type ProductFull struct {
	Product      *Product
	Sizes        []ProductSize
	Measurements []ProductMeasurement
	Media        []ProductMedia
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
	Other:     true,
}

// Category represents the category table
type Category struct {
	ID   int          `db:"id"`
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
	ID   int      `db:"id"`
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
	ID   int                 `db:"id"`
	Name MeasurementNameEnum `db:"name"`
}

type GenderEnum string

const (
	Male   GenderEnum = "male"
	Female GenderEnum = "female"
	Unisex GenderEnum = "unisex"
)

// ValidMeasurementNames is a map containing all the valid measurement names.
var ValidProductTargetGenders = map[GenderEnum]bool{
	Male:   true,
	Female: true,
	Unisex: true,
}

// Product represents the product table
type Product struct {
	ID        int       `db:"id"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
	ProductInsert
}

type ProductInsert struct {
	Preorder        sql.NullString      `db:"preorder"`
	Name            string              `db:"name"`
	Brand           string              `db:"brand"`
	SKU             string              `db:"sku"`
	Color           string              `db:"color"`
	ColorHex        string              `db:"color_hex"`
	CountryOfOrigin string              `db:"country_of_origin"`
	Thumbnail       string              `db:"thumbnail"`
	Price           decimal.Decimal     `db:"price"`
	SalePercentage  decimal.NullDecimal `db:"sale_percentage"`
	CategoryID      int                 `db:"category_id"`
	Description     string              `db:"description"`
	Hidden          sql.NullBool        `db:"hidden"`
	TargetGender    GenderEnum          `db:"target_gender"`
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
	ID        int             `db:"id"`
	Quantity  decimal.Decimal `db:"quantity"`
	ProductID int             `db:"product_id"`
	SizeID    int             `db:"size_id"`
}

// ProductSizes for insert represents the product_size table
type ProductSizeInsert struct {
	Quantity decimal.Decimal `db:"quantity"`
	SizeID   int             `db:"size_id"`
}

// SizeMeasurement represents the size_measurement table
type ProductMeasurement struct {
	ID                int             `db:"id"`
	ProductID         int             `db:"product_id"`
	ProductSizeID     int             `db:"product_size_id"`
	MeasurementNameID int             `db:"measurement_name_id"`
	MeasurementValue  decimal.Decimal `db:"measurement_value"`
}

// SizeMeasurement represents the size_measurement table
type ProductMeasurementInsert struct {
	MeasurementNameID int             `db:"measurement_name_id"`
	MeasurementValue  decimal.Decimal `db:"measurement_value"`
}

// ProductMedia represents the product_media table
type ProductMedia struct {
	ID        int       `db:"id"`
	CreatedAt time.Time `db:"created_at"`
	ProductID int       `db:"product_id"`
	ProductMediaInsert
}

// ProductMedia represents the product_media table
type ProductMediaInsert struct {
	FullSize   string `db:"full_size"`
	Thumbnail  string `db:"thumbnail"`
	Compressed string `db:"compressed"`
}

// ProductTag represents the product_tag table
type ProductTag struct {
	ID        int `db:"id"`
	ProductID int `db:"product_id"`
	ProductTagInsert
}

// ProductTag represents the product_tag table
type ProductTagInsert struct {
	Tag string `db:"tag"`
}

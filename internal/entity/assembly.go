package entity

import (
	"database/sql"
	"errors"

	"github.com/shopspring/decimal"
)

// ErrStyleAssemblyInvalid is returned when a style-assembly payload is malformed (missing/duplicate
// component, non-positive qty, a component that is not an auxiliary card, or a self-reference).
var ErrStyleAssemblyInvalid = errors.New("invalid style assembly")

// StyleAssembly is one stored line of a garment style's ASSEMBLY bill (WS7, §2.8): an auxiliary item
// (a tech card, purpose=auxiliary — a brand/care/size label, hangtag, sticker, dust bag…) that physically
// goes on/into the garment, with a quantity and print/position notes. It is distinct from packaging
// (WS2 packaging_recipe, on the shipment): assembly is on the garment and the component's output material
// is consumed in the garment's production run via the existing BOM/material path. The Component*/Output*/
// SizeName fields are resolved on read (List) for display and ignored on write.
type StyleAssembly struct {
	Id                  int             `db:"id"`
	StyleId             int             `db:"style_id"`
	ComponentTechCardId int             `db:"component_tech_card_id"`
	SizeId              sql.NullInt32   `db:"size_id"` // NULL = applies to all garment sizes
	Qty                 decimal.Decimal `db:"qty"`
	PrintNote           sql.NullString  `db:"print_note"`
	PositionNote        sql.NullString  `db:"position_note"`
	Active              bool            `db:"active"`
	LockVersion         int             `db:"lock_version"`
	CreatedBy           string          `db:"created_by"`
	UpdatedBy           string          `db:"updated_by"`
	// Resolved on read for display (List / packing spec):
	ComponentName       string         `db:"component_name"`         // auxiliary card name
	ComponentAuxSubtype sql.NullString `db:"component_aux_subtype"`  // auxiliary card aux_subtype
	OutputMaterialId    sql.NullInt32  `db:"output_material_id"`     // component's warehouse material (COGS link)
	OutputMaterialName  sql.NullString `db:"output_material_name"`   // resolved material name
	SizeName            sql.NullString `db:"size_name"`              // resolved when SizeId set
}

// StyleAssemblyInsert is one writable assembly line (full-replace per style; the style is carried by the
// UpsertStyleAssembly call, not per line).
type StyleAssemblyInsert struct {
	ComponentTechCardId int
	SizeId              sql.NullInt32
	Qty                 decimal.Decimal
	PrintNote           sql.NullString
	PositionNote        sql.NullString
	Active              bool
}

// OrderPackingSpec is the packer/QC-readable composition of an order (WS7 scope 3): the garments that
// ship, the on-garment assembly (labels/tags) to verify per line, and the packaging the whole order needs.
// It is a READ-ONLY projection; it neither reserves nor consumes anything (WS2 owns the reservation ledger).
type OrderPackingSpec struct {
	OrderUUID string
	Items     []OrderPackingSpecItem
	Packaging []OrderPackingSpecPackaging
}

// OrderPackingSpecItem is one garment line: the colourway/variant, its quantity, and the assembly bill
// (size-resolved to this line's variant size).
type OrderPackingSpecItem struct {
	OrderItemId int
	ProductId   int
	VariantId   int
	StyleId     int
	StyleName   string
	SKU         string
	SizeName    string
	Quantity    decimal.Decimal
	Assembly    []StyleAssembly
}

// OrderPackingSpecPackaging is one packaging material the order needs, resolved from WS2 packaging_recipe
// (product → style → global) and summed across the order.
type OrderPackingSpecPackaging struct {
	MaterialId   int
	MaterialName string
	MaterialUnit sql.NullString
	Qty          decimal.Decimal
}

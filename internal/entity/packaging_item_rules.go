package entity

import (
	"database/sql"

	"github.com/shopspring/decimal"
)

// The packaging items of a card are the one source for stock and for cost. These rules are what both
// read, so a piece cannot be priced by one meaning and written off by another.
const (
	PackagingItemKeyHanger = "hanger"
	PackagingItemKeyBox    = "box"
	// PackagingFoldingOnHanger is tech_card_packaging.folding_method of a piece shipped on a hanger.
	PackagingFoldingOnHanger = "on a hanger"
)

// PackagingItemShips says whether an item leaves with the piece: a hanger only when the piece ships
// on one, so switching a card to folded can never leave a hidden write-off behind.
func PackagingItemShips(key string, folding sql.NullString) bool {
	return key != PackagingItemKeyHanger || folding.String == PackagingFoldingOnHanger
}

// packagingShippingBoxUnits is the pieces one shipping box holds, when the item is that box.
func packagingShippingBoxUnits(key string, unitsPerBox sql.NullInt32) (decimal.Decimal, bool) {
	if key != PackagingItemKeyBox || !unitsPerBox.Valid || unitsPerBox.Int32 <= 0 {
		return decimal.Zero, false
	}
	return decimal.NewFromInt32(unitsPerBox.Int32), true
}

// PackagingItemOrderQty is what an order line of pieces needs of one item. once says the quantity
// counts a single time on the whole order. A shipping box with a known capacity goes in shares, the
// owner's rule: 1 piece takes 1/capacity, a full box of pieces takes exactly one.
func PackagingItemOrderQty(key string, qty int, perOrder bool, unitsPerBox sql.NullInt32, pieces decimal.Decimal) (need decimal.Decimal, once bool) {
	q := decimal.NewFromInt(int64(qty))
	if units, ok := packagingShippingBoxUnits(key, unitsPerBox); ok {
		return q.Mul(pieces).Div(units), false
	}
	if perOrder {
		return q, true
	}
	return q.Mul(pieces), false
}

// PackagingItemQtyPerPiece is an item's quantity in the cost of one piece. The cost does not know an
// order's size, so a per-order item counts as its quantity on every piece, the owner's rule: slightly
// over, never under.
func PackagingItemQtyPerPiece(key string, qty int, unitsPerBox sql.NullInt32) decimal.Decimal {
	q := decimal.NewFromInt(int64(qty))
	if units, ok := packagingShippingBoxUnits(key, unitsPerBox); ok {
		return q.Div(units)
	}
	return q
}

// PackagingAuxOutput is what an auxiliary card's warehouse output resolves from.
type PackagingAuxOutput struct {
	Variants []TechCardOutputVariant
	Legacy   AssemblyLegacyOutput
}

// AppliesToColorway says whether an item covers a colourway; no colourways named means all.
func (it *TechCardPackagingItem) AppliesToColorway(productID int) bool {
	if len(it.ColorwayIds) == 0 {
		return true
	}
	for _, id := range it.ColorwayIds {
		if id == productID {
			return true
		}
	}
	return false
}

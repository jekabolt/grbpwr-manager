package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// a tech card whose unit cost is materials(3 × unitPrice) + cmt, all in `ccy`.
func fxTestCard(ccy string, unitPrice, cmt string) *entity.TechCard {
	nd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}
	return &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		SizeQuantities: []entity.TechCardSizeQuantity{{SizeId: 4, OrderQty: 100}},
		BomItems: []entity.TechCardBomItem{{
			Section: entity.BomSectionFabric, Name: "shell",
			UnitPrice: nd(unitPrice), Currency: sql.NullString{String: ccy, Valid: ccy != ""},
		}},
		Colorways: []entity.TechCardColorway{{Name: "Black", Usages: []entity.TechCardColorwayUsage{
			{BomItemIndex: sql.NullInt32{Int32: 0, Valid: true}, Quantity: nd("3")},
		}}},
		Costing: &entity.TechCardCosting{CmtCost: nd(cmt), Currency: sql.NullString{String: ccy, Valid: ccy != ""}},
	}}
}

func TestComputeTechCardUnitCostBaseRollup(t *testing.T) {
	// EUR costing, base EUR: unit = 3*2 + 10 = 16, no rate needed.
	card := fxTestCard("EUR", "2", "10")
	fxEUR := CostingFx{Base: "EUR"}
	unit, ccy := ComputeTechCardUnitCost(card, fxEUR)
	if !unit.Valid || unit.Decimal.String() != "16" || ccy != "EUR" {
		t.Fatalf("EUR costing: got %v %q, want 16 EUR", unit, ccy)
	}

	// USD costing with a USD->EUR rate of 0.9: unit = 16 USD -> 14.40 EUR.
	usd := fxTestCard("USD", "2", "10")
	fxUSD := CostingFx{Base: "EUR", ToBase: map[string]decimal.Decimal{"USD": decimal.RequireFromString("0.9")}}
	unit, ccy = ComputeTechCardUnitCost(usd, fxUSD)
	if !unit.Valid || ccy != "EUR" {
		t.Fatalf("USD costing: got valid=%v ccy=%q, want valid EUR", unit.Valid, ccy)
	}
	if unit.Decimal.String() != "14.4" {
		t.Fatalf("USD costing base rollup: got %s, want 14.4", unit.Decimal.String())
	}

	// USD costing but no rate: base rollup incomplete, falls back to USD unit cost, which the
	// seed then skips because it is not the base currency.
	unit, ccy = ComputeTechCardUnitCost(usd, CostingFx{Base: "EUR"})
	if !unit.Valid || ccy != "USD" || unit.Decimal.String() != "16" {
		t.Fatalf("USD costing no rate: got %v %q, want fallback 16 USD", unit, ccy)
	}
}

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

// TestComputeTechCardCostBreakdownBase checks the per-component base decomposition seeded onto
// product.cost_breakdown: components sum (× defect) to the same unit cost ComputeTechCardUnitCost
// returns, folding via the same FX, and it is unavailable exactly when the base rollup is.
func TestComputeTechCardCostBreakdownBase(t *testing.T) {
	// EUR costing: materials 3×2 = 6, cmt 10, no defect ⇒ components sum to unit cost 16.
	card := fxTestCard("EUR", "2", "10")
	bd, ok := ComputeTechCardCostBreakdownBase(card, CostingFx{Base: "EUR"})
	if !ok {
		t.Fatalf("EUR breakdown: got ok=false, want true")
	}
	if bd.Materials.String() != "6" || bd.Cmt.String() != "10" {
		t.Fatalf("EUR breakdown: got materials=%s cmt=%s, want 6/10", bd.Materials, bd.Cmt)
	}
	if bd.Hardware.String() != "0" || bd.Packaging.String() != "0" || bd.Logistics.String() != "0" || bd.Overhead.String() != "0" {
		t.Fatalf("EUR breakdown: unset articles should be 0, got hw=%s pkg=%s log=%s ovh=%s",
			bd.Hardware, bd.Packaging, bd.Logistics, bd.Overhead)
	}
	sum := bd.Materials.Add(bd.Cmt).Add(bd.Hardware).Add(bd.Packaging).Add(bd.Logistics).Add(bd.Overhead)
	if sum.String() != "16" {
		t.Fatalf("EUR breakdown: components sum %s, want 16 (= unit cost)", sum)
	}

	// USD costing, USD->EUR 0.9: every component folds to base — materials 5.4, cmt 9, sum 14.4.
	usd := fxTestCard("USD", "2", "10")
	fxUSD := CostingFx{Base: "EUR", ToBase: map[string]decimal.Decimal{"USD": decimal.RequireFromString("0.9")}}
	bd, ok = ComputeTechCardCostBreakdownBase(usd, fxUSD)
	if !ok {
		t.Fatalf("USD breakdown: got ok=false, want true")
	}
	if bd.Materials.String() != "5.4" || bd.Cmt.String() != "9" {
		t.Fatalf("USD breakdown: got materials=%s cmt=%s, want 5.4/9", bd.Materials, bd.Cmt)
	}

	// USD costing, no rate: breakdown unavailable — same condition as the base rollup.
	if _, ok := ComputeTechCardCostBreakdownBase(usd, CostingFx{Base: "EUR"}); ok {
		t.Fatalf("USD no-rate breakdown: got ok=true, want false")
	}

	// No costing / no colourway: unavailable.
	if _, ok := ComputeTechCardCostBreakdownBase(&entity.TechCard{}, CostingFx{Base: "EUR"}); ok {
		t.Fatalf("empty card breakdown: got ok=true, want false")
	}
}

// TestComputeTechCardDevCostSummary checks the task-14 dev-cost roll-up: base total over foldable
// rows, per-kind split, has_unconverted flag, and the amortized unit_cost_with_dev = production
// unit cost + dev_total / Σ order_qty.
func TestComputeTechCardDevCostSummary(t *testing.T) {
	nd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}
	dec := decimal.RequireFromString

	// EUR card: unit cost 16 (materials 3×2 + cmt 10), size run 100 units.
	card := fxTestCard("EUR", "2", "10")
	expenses := []entity.TechCardDevExpense{
		{Kind: "sample", Amount: dec("100"), Currency: "EUR", AmountBase: nd("100")},
		{Kind: "labour", Amount: dec("50"), Currency: "EUR", AmountBase: nd("50")},
		{Kind: "materials", Amount: dec("30"), Currency: "USD"}, // no amount_base → unconverted
	}
	sum := ComputeTechCardDevCostSummary(card, expenses, CostingFx{Base: "EUR"})

	if sum.TotalBase.Value != "150" {
		t.Fatalf("total_base: got %s, want 150 (unconverted row excluded)", sum.TotalBase.Value)
	}
	if !sum.HasUnconverted {
		t.Fatalf("has_unconverted: got false, want true (the USD row has no rate)")
	}
	if sum.OrderQty != 100 {
		t.Fatalf("order_qty: got %d, want 100", sum.OrderQty)
	}
	// 16 + 150/100 = 17.5.
	if sum.UnitCostWithDev == nil || sum.UnitCostWithDev.Value != "17.5" {
		t.Fatalf("unit_cost_with_dev: got %v, want 17.5", sum.UnitCostWithDev)
	}
	byKind := map[string]string{}
	for _, k := range sum.ByKind {
		byKind[k.Kind] = k.AmountBase.Value
	}
	if byKind["sample"] != "100" || byKind["labour"] != "50" {
		t.Fatalf("by_kind: got %v, want sample=100 labour=50", byKind)
	}
	if _, ok := byKind["materials"]; ok {
		t.Fatalf("by_kind: unconverted materials row must not appear")
	}
}

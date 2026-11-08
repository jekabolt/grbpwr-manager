package entity

import (
	"database/sql"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func spDec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func spLegacyLine(qty int) ProductionRunLine {
	return ProductionRunLine{LineKey: "l1", PlannedQty: qty}
}

func spVariantLine(key string, variant int32, qty int) ProductionRunLine {
	return ProductionRunLine{LineKey: key, PlannedQty: qty,
		OutputVariantId: sql.NullInt32{Int32: variant, Valid: true}}
}

func spPrice(mat int, price, ccy string) MaterialPrice {
	return MaterialPrice{MaterialId: mat, Price: spDec(price), Currency: ccy, Source: MaterialPriceSourceManual,
		ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func TestSupplierPricePicksNewestManualOrPurchase(t *testing.T) {
	d := func(day int) time.Time { return time.Date(2026, 3, day, 0, 0, 0, 0, time.UTC) }
	prices := []MaterialPrice{
		{Price: spDec("9.99"), Currency: "EUR", ValidFrom: d(20), Source: MaterialPriceSourceProductionRun},
		{Price: spDec("1.50"), Currency: "EUR", ValidFrom: d(1), Source: MaterialPriceSourceManual},
		{Price: spDec("2.10"), Currency: "usd", ValidFrom: d(10), Source: MaterialPriceSourcePurchase},
		{Price: spDec("1.80"), Currency: "EUR", ValidFrom: d(10), Source: MaterialPriceSourceManual},
	}
	asOf := d(25)
	p, ok := SupplierPrice(prices, asOf)
	require.True(t, ok)
	// production_run is never a supplier price; the tie on 10 March goes to the currency first in order.
	require.True(t, p.Price.Equal(spDec("1.80")))
	require.Equal(t, "EUR", p.Currency)

	_, ok = SupplierPrice([]MaterialPrice{{Price: spDec("3"), Currency: "EUR", Source: MaterialPriceSourceProductionRun}}, asOf)
	require.False(t, ok)
	_, ok = SupplierPrice(nil, asOf)
	require.False(t, ok)
}

func TestSupplierPriceIgnoresAPriceDatedAhead(t *testing.T) {
	today := time.Date(2026, 3, 15, 9, 30, 0, 0, time.UTC)
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }
	prices := []MaterialPrice{
		{Price: spDec("2.00"), Currency: "EUR", ValidFrom: day(3, 1), Source: MaterialPriceSourceManual},
		{Price: spDec("3.00"), Currency: "EUR", ValidFrom: day(4, 1), Source: MaterialPriceSourcePurchase},
	}
	p, ok := SupplierPrice(prices, today)
	require.True(t, ok)
	require.True(t, p.Price.Equal(spDec("2.00")), "next month's price is not today's: %s", p.Price)

	// A price dated today is in force.
	prices = append(prices, MaterialPrice{Price: spDec("2.40"), Currency: "EUR", ValidFrom: day(3, 15), Source: MaterialPriceSourceManual})
	p, ok = SupplierPrice(prices, today)
	require.True(t, ok)
	require.True(t, p.Price.Equal(spDec("2.40")), p.Price.String())

	// Only a price dated ahead → none yet.
	_, ok = SupplierPrice(prices[1:2], today)
	require.False(t, ok)
}

func TestComputeSupplierPriceSeedLegacy(t *testing.T) {
	seed := ComputeSupplierPriceSeed([]ProductionRunLine{spLegacyLine(500)}, nil,
		sql.NullInt64{Int64: 7, Valid: true}, map[int]MaterialPrice{7: spPrice(7, "1.80", "EUR")})
	require.Len(t, seed.Articles, 1)
	a := seed.Articles[0]
	require.Equal(t, ProductionRunCostMaterials, a.Kind)
	require.Equal(t, SupplierPriceCostDescription, a.Description.String)
	require.True(t, a.Amount.Equal(spDec("900")), a.Amount.String())
	require.Equal(t, "EUR", a.Currency)
	require.False(t, a.AmountBase.Valid, "the store folds it")
	require.Equal(t, []SupplierPriceSeedBasis{{MaterialId: 7, Qty: 500, Price: spDec("1.80"), Currency: "EUR"}}, seed.Basis)
}

func TestComputeSupplierPriceSeedPerColourAndCurrency(t *testing.T) {
	lines := []ProductionRunLine{
		spVariantLine("a", 1, 100), spVariantLine("b", 2, 50), spVariantLine("c", 1, 20), spVariantLine("d", 3, 0),
	}
	variants := map[int]int{1: 11, 2: 12, 3: 13}
	prices := map[int]MaterialPrice{11: spPrice(11, "1.25", "EUR"), 12: spPrice(12, "2.0050", "usd")}
	seed := ComputeSupplierPriceSeed(lines, variants, sql.NullInt64{}, prices)
	require.Len(t, seed.Articles, 2)
	// Each line takes its own material's price; a zero-qty line asks for none (13 has no price).
	require.Equal(t, "EUR", seed.Articles[0].Currency)
	require.True(t, seed.Articles[0].Amount.Equal(spDec("150")), seed.Articles[0].Amount.String())
	require.Equal(t, "USD", seed.Articles[1].Currency)
	require.True(t, seed.Articles[1].Amount.Equal(spDec("100.25")), seed.Articles[1].Amount.String())
}

func TestComputeSupplierPriceSeedAllOrNothing(t *testing.T) {
	legacy := sql.NullInt64{Int64: 7, Valid: true}
	priced := map[int]MaterialPrice{7: spPrice(7, "1.80", "EUR")}
	cases := map[string]SupplierPriceSeed{
		"no price":        ComputeSupplierPriceSeed([]ProductionRunLine{spLegacyLine(500)}, nil, legacy, nil),
		"nothing planned": ComputeSupplierPriceSeed([]ProductionRunLine{spLegacyLine(0)}, nil, legacy, priced),
		"no output":       ComputeSupplierPriceSeed([]ProductionRunLine{spLegacyLine(500)}, nil, sql.NullInt64{}, priced),
		"one colour unpriced": ComputeSupplierPriceSeed(
			[]ProductionRunLine{spVariantLine("a", 1, 10), spVariantLine("b", 2, 10)},
			map[int]int{1: 7, 2: 8}, sql.NullInt64{}, priced),
		"unknown colour": ComputeSupplierPriceSeed([]ProductionRunLine{spVariantLine("a", 9, 10)},
			map[int]int{1: 7}, sql.NullInt64{}, priced),
	}
	for name, seed := range cases {
		require.Empty(t, seed.Articles, name)
	}
}

// spSeed is a seed as the store hands it over: already folded to base (EUR here, so base = amount).
func spSeed(amount string) SupplierPriceSeed {
	return SupplierPriceSeed{
		Articles: []ProductionRunCost{{Kind: ProductionRunCostMaterials,
			Description: sql.NullString{String: SupplierPriceCostDescription, Valid: true},
			Amount:      spDec(amount), Currency: "EUR",
			AmountBase: decimal.NullDecimal{Decimal: spDec(amount), Valid: true}}},
		Basis: []SupplierPriceSeedBasis{{MaterialId: 7, Qty: 1, Price: spDec(amount), Currency: "EUR"}},
	}
}

func spRecord(amount string) *SupplierPriceSeedRecord {
	return &SupplierPriceSeedRecord{Articles: []SupplierPriceSeedArticle{{Amount: spDec(amount), Currency: "EUR",
		AmountBase: decimal.NullDecimal{Decimal: spDec(amount), Valid: true}}}}
}

func spStored(amount string) ProductionRunCost {
	return ProductionRunCost{Kind: ProductionRunCostMaterials,
		Description: sql.NullString{String: SupplierPriceCostDescription, Valid: true},
		Amount:      spDec(amount), Currency: "EUR",
		AmountBase: decimal.NullDecimal{Decimal: spDec(amount), Valid: true}}
}

func TestReconcileSupplierPriceCostFirstSeed(t *testing.T) {
	freight := ProductionRunCost{Kind: ProductionRunCostLogistics, Amount: spDec("40"), Currency: "EUR"}
	costs, rec := ReconcileSupplierPriceCost([]ProductionRunCost{freight}, spSeed("900"), nil)
	require.NotNil(t, rec)
	require.Len(t, costs, 2)
	require.True(t, costs[1].Amount.Equal(spDec("900")))
	require.Equal(t, spRecord("900").Articles, rec.Articles)
	require.NotEmpty(t, rec.Basis)

	// No price → no article and nothing recorded.
	costs, rec = ReconcileSupplierPriceCost([]ProductionRunCost{freight}, SupplierPriceSeed{}, nil)
	require.Nil(t, rec)
	require.Len(t, costs, 1)

	// A materials article typed by the operator is the same goods: never counted twice.
	typed := ProductionRunCost{Kind: ProductionRunCostMaterials, Amount: spDec("912.40"), Currency: "EUR"}
	costs, rec = ReconcileSupplierPriceCost([]ProductionRunCost{typed}, spSeed("900"), nil)
	require.Nil(t, rec)
	require.Len(t, costs, 1)

	// A removal record does not block a later seed.
	costs, rec = ReconcileSupplierPriceCost(nil, spSeed("900"), &SupplierPriceSeedRecord{Articles: []SupplierPriceSeedArticle{}})
	require.NotNil(t, rec)
	require.Len(t, costs, 1)
}

func TestReconcileSupplierPriceCostFollowsUntouchedArticle(t *testing.T) {
	freight := ProductionRunCost{Kind: ProductionRunCostLogistics, Amount: spDec("40"), Currency: "EUR"}
	stored := []ProductionRunCost{freight, spStored("900")}

	// Same basis → nothing to do, the stored base stays.
	costs, rec := ReconcileSupplierPriceCost(stored, spSeed("900"), spRecord("900"))
	require.Nil(t, rec)
	require.Equal(t, stored, costs)

	// Plan moved, article untouched → replaced, freight kept.
	costs, rec = ReconcileSupplierPriceCost(stored, spSeed("1080"), spRecord("900"))
	require.NotNil(t, rec)
	require.Len(t, costs, 2)
	require.Equal(t, ProductionRunCostLogistics, costs[0].Kind)
	require.True(t, costs[1].Amount.Equal(spDec("1080")))
	require.Equal(t, spRecord("1080").Articles, rec.Articles)

	// The basis is gone (price removed, plan zeroed, card made by us) → the untouched article goes.
	costs, rec = ReconcileSupplierPriceCost(stored, SupplierPriceSeed{}, spRecord("900"))
	require.NotNil(t, rec)
	require.Empty(t, rec.Articles)
	require.Equal(t, []ProductionRunCost{freight}, costs)
}

func TestReconcileSupplierPriceCostHandsOffOnceEdited(t *testing.T) {
	edits := map[string]func(*ProductionRunCost){
		"amount":      func(c *ProductionRunCost) { c.Amount = spDec("912.40") },
		"currency":    func(c *ProductionRunCost) { c.Currency = "USD" },
		"description": func(c *ProductionRunCost) { c.Description.String = "invoice 118" },
		"kind":        func(c *ProductionRunCost) { c.Kind = ProductionRunCostOther },
		"document":    func(c *ProductionRunCost) { c.DocumentRef = sql.NullString{String: "INV-1", Valid: true} },
		"supplier":    func(c *ProductionRunCost) { c.SupplierId = sql.NullInt64{Int64: 3, Valid: true} },
		"vat":         func(c *ProductionRunCost) { c.VatRate = decimal.NullDecimal{Decimal: spDec("23"), Valid: true} },
		"paid":        func(c *ProductionRunCost) { c.ApStatus = sql.NullString{String: "paid", Valid: true} },
		// The invoice's own rate typed over the folded base: the amount is the seed's, the money is not.
		"base override": func(c *ProductionRunCost) { c.AmountBase = decimal.NullDecimal{Decimal: spDec("905.12"), Valid: true} },
		"base cleared":  func(c *ProductionRunCost) { c.AmountBase = decimal.NullDecimal{} },
	}
	for name, edit := range edits {
		c := spStored("900")
		edit(&c)
		costs, rec := ReconcileSupplierPriceCost([]ProductionRunCost{c}, spSeed("1080"), spRecord("900"))
		require.Nil(t, rec, name)
		require.Equal(t, []ProductionRunCost{c}, costs, name)
	}
	// Deleted by the operator → not re-seeded either.
	costs, rec := ReconcileSupplierPriceCost(nil, spSeed("1080"), spRecord("900"))
	require.Nil(t, rec)
	require.Empty(t, costs)

	// accrued and an incurred date are not edits.
	c := spStored("900")
	c.ApStatus = sql.NullString{String: "accrued", Valid: true}
	c.IncurredAt = sql.NullTime{Time: time.Now(), Valid: true}
	_, rec = ReconcileSupplierPriceCost([]ProductionRunCost{c}, spSeed("1080"), spRecord("900"))
	require.NotNil(t, rec)
}

func TestSupplierPriceSeedOpen(t *testing.T) {
	for s := range ValidProductionRunStatuses {
		want := s == ProductionRunDraft || s == ProductionRunPlanned || s == ProductionRunInProgress
		require.Equal(t, want, SupplierPriceSeedOpen(s), string(s))
	}
}

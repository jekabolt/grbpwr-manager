package order

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

const eurCurrency = "EUR"

// eurUnitPrices returns the base EUR unit price per product id.
func eurUnitPrices(ctx context.Context, db dependency.DB, productIDs []int) (map[int]decimal.Decimal, error) {
	out := map[int]decimal.Decimal{}
	if len(productIDs) == 0 {
		return out, nil
	}
	type row struct {
		ProductID int             `db:"product_id"`
		Price     decimal.Decimal `db:"price"`
	}
	q := `SELECT product_id, price FROM product_price WHERE currency = 'EUR' AND product_id IN (:ids)`
	rows, err := storeutil.QueryListNamed[row](ctx, db, q, map[string]any{"ids": productIDs})
	if err != nil {
		return nil, fmt.Errorf("load EUR prices: %w", err)
	}
	for _, r := range rows {
		out[r.ProductID] = r.Price
	}
	return out, nil
}

// computeTotalPriceEUR derives the EUR-equivalent total of an order for loyalty
// spend. The shop has no live FX — prices are hand-set per currency — so the EUR
// snapshot is built from the EUR product prices + EUR shipping, scaled by the
// same promo/discount ratio the order's own-currency total carried.
//
// For EUR orders the snapshot is exactly the order total. If any item lacks an
// EUR price the snapshot is left invalid (NULL) and that order won't count.
func computeTotalPriceEUR(
	ctx context.Context,
	db dependency.DB,
	order *entity.Order,
	items []entity.OrderItemInsert,
	carrier *entity.ShipmentCarrier,
	shipmentCost decimal.Decimal,
	freeShipping bool,
) (decimal.NullDecimal, error) {
	if strings.EqualFold(order.Currency, eurCurrency) {
		return decimal.NullDecimal{Decimal: order.TotalPriceDecimal(), Valid: true}, nil
	}

	productIDs := make([]int, 0, len(items))
	for _, it := range items {
		productIDs = append(productIDs, it.ProductId)
	}
	eurPrices, err := eurUnitPrices(ctx, db, productIDs)
	if err != nil {
		return decimal.NullDecimal{}, err
	}

	goodsOrder := decimal.Zero
	goodsEUR := decimal.Zero
	hundred := decimal.NewFromInt(100)
	for _, it := range items {
		qty := it.QuantityDecimal()
		goodsOrder = goodsOrder.Add(it.ProductPriceWithSaleDecimal().Mul(qty))

		base, ok := eurPrices[it.ProductId]
		if !ok {
			// Cannot build a faithful EUR snapshot for this order.
			return decimal.NullDecimal{}, nil
		}
		unit := base
		if sale := it.ProductSalePercentageDecimal(); sale.GreaterThan(decimal.Zero) {
			unit = base.Mul(hundred.Sub(sale).Div(hundred))
		}
		goodsEUR = goodsEUR.Add(unit.Mul(qty))
	}

	shipOrder := decimal.Zero
	shipEUR := decimal.Zero
	if !freeShipping && carrier != nil {
		shipOrder = shipmentCost
		if p, err := carrier.PriceDecimal(eurCurrency); err == nil {
			shipEUR = p
		}
	}

	eurTotal := goodsEUR.Add(shipEUR)
	baseOrder := goodsOrder.Add(shipOrder)
	if baseOrder.GreaterThan(decimal.Zero) {
		// Scale by the realized discount ratio (handles promo codes / free shipping).
		ratio := order.TotalPriceDecimal().Div(baseOrder)
		eurTotal = eurTotal.Mul(ratio)
	}
	return decimal.NullDecimal{Decimal: eurTotal.Round(2), Valid: true}, nil
}

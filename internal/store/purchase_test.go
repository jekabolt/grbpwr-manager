package store

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

func TestValidateOrder(t *testing.T) {
	db := newTestDB(t)
	os := db.Order()
	ps := db.Products()
	prs := db.Purchase()
	ctx := context.Background()

	prd := getTestProd(1)[0]
	categories := []string{}
	for _, c := range prd.Categories {
		categories = append(categories, c.Category)
	}

	err := ps.AddProduct(ctx, prd.ProductInfo.Name, prd.ProductInfo.Description, prd.ProductInfo.Preorder, prd.AvailableSizes, prd.Price, prd.Media, categories)
	assert.NoError(t, err)

	limit, offset := int32(10), int32(0)
	sortFactors := []dto.SortFactor{}
	filterConditions := []dto.FilterCondition{}

	products, err := ps.GetProductsPaged(ctx, limit, offset, sortFactors, filterConditions, false)
	assert.NoError(t, err)

	order := getTestOrders(1, products)[0]

	items := []dto.Item{
		{
			ID:   products[0].ProductInfo.Id,
			Size: "S",
			// overflows available size to trigger error
			Quantity: prd.AvailableSizes.S + 1,
		},
	}
	order.Items = items
	email := "test"
	order.Buyer.Email = email

	err = db.Tx(ctx, func(ctx context.Context, store dependency.Repository) error {
		_, err := store.Order().CreateOrder(ctx, order)
		return err
	})
	assert.NoError(t, err)

	ordersByEmail, err := os.OrdersByEmail(ctx, email)
	assert.NoError(t, err)
	assert.Len(t, ordersByEmail, 1)

	orderID := ordersByEmail[0].ID

	ok, err := prs.ValidateOrder(ctx, orderID)
	assert.False(t, ok)
	assert.NoError(t, err)

	// fix order items
	items[0].Quantity = prd.AvailableSizes.S - 1
	err = os.UpdateOrderItems(ctx, orderID, items)
	assert.NoError(t, err)

	ok, err = prs.ValidateOrder(ctx, orderID)
	assert.True(t, ok)
	assert.NoError(t, err)

}

func TestPurchase(t *testing.T) {
	db := newTestDB(t)
	os := db.Order()
	ps := db.Products()
	prs := db.Purchase()
	ctx := context.Background()

	prd := getTestProd(1)[0]

	categories := []string{}
	for _, c := range prd.Categories {
		categories = append(categories, c.Category)
	}
	err := ps.AddProduct(ctx, prd.ProductInfo.Name, prd.ProductInfo.Description, prd.ProductInfo.Preorder, prd.AvailableSizes, prd.Price, prd.Media, categories)
	assert.NoError(t, err)

	limit, offset := int32(10), int32(0)
	sortFactors := []dto.SortFactor{}
	filterConditions := []dto.FilterCondition{}

	products, err := ps.GetProductsPaged(ctx, limit, offset, sortFactors, filterConditions, false)
	assert.NoError(t, err)

	order := getTestOrders(1, products)[0]

	items := []dto.Item{
		{
			ID:       products[0].ProductInfo.Id,
			Size:     "S",
			Quantity: prd.AvailableSizes.S,
		},
	}
	order.Items = items
	email := "test"
	order.Buyer.Email = email

	err = db.Tx(ctx, func(ctx context.Context, store dependency.Repository) error {
		_, err := store.Order().CreateOrder(ctx, order)
		return err
	})
	assert.NoError(t, err)

	ordersByEmail, err := os.OrdersByEmail(ctx, email)
	assert.NoError(t, err)
	assert.Len(t, ordersByEmail, 1)

	orderID := ordersByEmail[0].ID
	total := decimal.Decimal{}
	err = db.Tx(ctx, func(ctx context.Context, store dependency.Repository) error {
		total, err = store.Order().UpdateOrderTotalByCurrency(ctx, orderID, dto.EUR, nil)
		return err
	})
	assert.NoError(t, err)

	p := &dto.Payment{
		Method:            dto.CardPayment,
		Currency:          dto.EUR,
		TransactionID:     "txid",
		TransactionAmount: total,
		Payer:             "payer",
		Payee:             "payee",
		IsTransactionDone: true,
	}

	err = prs.Acquire(ctx, orderID, p)
	assert.NoError(t, err)
}

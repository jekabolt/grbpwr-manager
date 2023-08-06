package store

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

func getTestOrders(count int, prds []dto.Product) []*dto.Order {
	orders := []*dto.Order{}

	for i := 0; i < count; i++ {
		order := &dto.Order{
			Buyer:   getTestBuyer(),
			Placed:  time.Now(),
			Items:   getTestItems(rand.Intn(len(prds))+1, prds),
			Payment: getTestPayment(),
			Shipment: &dto.Shipment{
				Carrier:              "DHL",
				TrackingCode:         fmt.Sprintf("TRACK-%d", i+1),
				ShippingDate:         time.Now().AddDate(0, 0, 2),
				EstimatedArrivalDate: time.Now().AddDate(0, 0, 5),
			},
			TotalPrice: decimal.NewFromFloat(0),
			Status:     getRandomOrderStatus(),
		}
		orders = append(orders, order)
	}

	return orders
}

func getTestBuyer() *dto.Buyer {
	return &dto.Buyer{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john.doe@example.com",
		Phone:     "+1234567890",
		BillingAddress: &dto.Address{
			Street:          "123 Billing St",
			HouseNumber:     "Apt 4B",
			City:            "New York",
			State:           "NY",
			Country:         "USA",
			PostalCode:      "10001",
			ApartmentNumber: "",
		},
		ShippingAddress: &dto.Address{
			Street:          "456 Shipping St",
			HouseNumber:     "",
			City:            "New York",
			State:           "NY",
			Country:         "USA",
			PostalCode:      "10002",
			ApartmentNumber: "Suite 10",
		},
		ReceivePromoEmails: true,
	}
}

func getTestPayment() *dto.Payment {
	return &dto.Payment{
		Method:            dto.USDC,
		Currency:          dto.USDCrypto,
		TransactionID:     "12345",
		TransactionAmount: decimal.NewFromFloat(100.0),
		Payer:             "John Doe",
		Payee:             "Example Store",
		IsTransactionDone: true,
	}
}

func getTestItems(count int, prds []dto.Product) []dto.Item {

	items := []dto.Item{}
	for i := 0; i < count; i++ {
		// rand elem in prds
		n := rand.Int() % len(prds)
		item := dto.Item{
			ID:       prds[n].Id,
			Quantity: count,
			Size:     "M",
		}
		items = append(items, item)
	}
	return items
}

func getRandomOrderStatus() dto.OrderStatus {
	statuses := []dto.OrderStatus{
		dto.OrderPlaced,
		dto.OrderConfirmed,
		dto.OrderShipped,
		dto.OrderDelivered,
		dto.OrderCancelled,
		dto.OrderRefunded,
	}

	index := rand.Intn(len(statuses))
	return statuses[index]
}

func TestOrdersStore_CreateOrder(t *testing.T) {
	db := newTestDB(t)
	os := db.Order()
	ps := db.Products()
	ctx := context.Background()

	prds := getTestProd(10)
	for _, prd := range prds {
		err := ps.AddProduct(ctx, prd)
		assert.NoError(t, err)
	}

	limit, offset := 10, 0
	sortFactors := []dto.SortFactor{}
	filterConditions := []dto.FilterCondition{}

	products, err := ps.GetProductsPaged(ctx, limit, offset, sortFactors, filterConditions)
	assert.NoError(t, err)

	orders := getTestOrders(10, products)

	for _, order := range orders {
		_, err := os.CreateOrder(ctx, order)
		assert.Error(t, err)
		err = db.Tx(ctx, func(ctx context.Context, store dependency.Repository) error {
			_, err := store.Order().CreateOrder(ctx, order)
			return err
		})
		assert.NoError(t, err)
	}

	email := getTestBuyer().Email

	ordersByEmail, err := os.OrdersByEmail(ctx, email)
	assert.NoError(t, err)

	assert.Equal(t, len(ordersByEmail), len(orders))

	for i, o := range ordersByEmail {
		assert.Equal(t, o.Items, orders[i].Items)
	}

	o, err := os.GetOrder(ctx, ordersByEmail[0].ID)
	assert.NoError(t, err)
	assert.Equal(t, *o, ordersByEmail[0])

	err = db.Tx(ctx, func(ctx context.Context, store dependency.Repository) error {
		return store.Order().UpdateOrderStatus(ctx, ordersByEmail[0].ID, dto.OrderCancelled)
	})
	assert.NoError(t, err)

	// check if cancelled orders is not visible
	ordersByEmail, err = os.OrdersByEmail(ctx, email)
	assert.NoError(t, err)
	assert.Equal(t, len(orders)-1, len(ordersByEmail))

	o = &ordersByEmail[0]
	p := &dto.Payment{
		Method:            dto.CardPayment,
		Currency:          dto.EUR,
		TransactionID:     "txid",
		TransactionAmount: decimal.NewFromFloat(0.5),
		Payer:             "payer",
		Payee:             "payee",
		IsTransactionDone: true,
	}

	// tx amount is less than order amount
	err = db.Tx(ctx, func(ctx context.Context, store dependency.Repository) error {
		return store.Order().OrderPaymentDone(ctx, o.ID, p)
	})
	assert.Error(t, err)

	// tx amount is equal to order amount
	p.TransactionAmount = o.TotalPrice
	err = db.Tx(ctx, func(ctx context.Context, store dependency.Repository) error {
		return store.Order().OrderPaymentDone(ctx, o.ID, p)
	})
	assert.NoError(t, err)

	o, err = os.GetOrder(ctx, o.ID)
	assert.NoError(t, err)

	assert.Equal(t, p.Method, o.Payment.Method)
	assert.Equal(t, p.Currency, o.Payment.Currency)
	assert.Equal(t, p.TransactionID, o.Payment.TransactionID)
	assert.True(t, p.TransactionAmount.Equal(o.Payment.TransactionAmount))
	assert.Equal(t, p.Payer, o.Payment.Payer)
	assert.Equal(t, p.Payee, o.Payment.Payee)
	assert.Equal(t, p.IsTransactionDone, o.Payment.IsTransactionDone)

	newItems := o.Items[1:]

	err = os.UpdateOrderItems(ctx, o.ID, newItems)
	assert.NoError(t, err)

	o, err = os.GetOrder(ctx, o.ID)
	assert.NoError(t, err)

	assert.Equal(t, newItems, o.Items)

}

func TestOrdersStore_Promo(t *testing.T) {
	db := newTestDB(t)
	os := db.Order()
	ps := db.Products()
	prs := db.Promo()
	ctx := context.Background()

	prd := getTestProd(1)[0]
	err := ps.AddProduct(ctx, prd)
	assert.NoError(t, err)

	limit, offset := 10, 0
	sortFactors := []dto.SortFactor{}
	filterConditions := []dto.FilterCondition{}

	products, err := ps.GetProductsPaged(ctx, limit, offset, sortFactors, filterConditions)
	assert.NoError(t, err)

	orders := getTestOrders(1, products)

	order := &dto.Order{}
	err = db.Tx(ctx, func(ctx context.Context, store dependency.Repository) error {
		order, err = store.Order().CreateOrder(ctx, orders[0])
		return err
	})
	assert.NoError(t, err)

	order, err = os.GetOrder(ctx, order.ID)
	assert.NoError(t, err)
	// check wether order total price is equal to product price + shipment cost
	assert.True(t, order.TotalPrice.Equal(prd.Price.USDC.Add(order.Shipment.Cost)))

	// apply wrong promo code
	err = os.ApplyPromoCode(ctx, order.ID, "fake promo")
	assert.Error(t, err)

	// add promo code

	err = prs.AddPromo(ctx, promoFreeShip)
	assert.NoError(t, err)
	err = prs.AddPromo(ctx, promoSale)
	assert.NoError(t, err)

	// apply existing promo code with free shipping
	err = os.ApplyPromoCode(ctx, order.ID, promoFreeShip.Code)
	assert.NoError(t, err)

	orderWPromo, err := os.GetOrder(ctx, order.ID)
	assert.NoError(t, err)

	// check if original order total price - shipment cost == new total price
	assert.True(t, order.TotalPrice.Sub(order.Shipment.Cost).Equal(orderWPromo.TotalPrice))

	// apply existing promo code with 10% off
	err = os.ApplyPromoCode(ctx, order.ID, promoSale.Code)
	assert.NoError(t, err)

	orderWPromo, err = os.GetOrder(ctx, order.ID)
	assert.NoError(t, err)

	salePrice := order.TotalPrice.Sub(order.Shipment.Cost)
	salePrice = salePrice.Mul(decimal.NewFromFloat(1).Sub(promoSale.Sale.Div(decimal.NewFromFloat(100))))
	salePrice = salePrice.Add(order.Shipment.Cost)

	// check if original order total price - shipment cost == new total price
	assert.True(t, salePrice.Equal(orderWPromo.TotalPrice))
}

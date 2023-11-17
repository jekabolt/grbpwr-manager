package store

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

func getRandomPaymentMethod(db *MYSQLStore) (*entity.PaymentMethod, error) {
	pms := db.cache.GetDict().PaymentMethods
	if len(pms) == 0 {
		return nil, fmt.Errorf("no payment methods found")
	}

	index := rand.Intn(len(pms))
	pm := pms[index]

	// Safe return by copying the struct before taking its address
	copiedPM := pm
	return &copiedPM, nil
}

func getRandomShipmentCarrier(db *MYSQLStore) (*entity.ShipmentCarrier, error) {
	scs := db.cache.GetDict().ShipmentCarriers
	if len(scs) == 0 {
		return nil, fmt.Errorf("no shipment carriers found")
	}

	index := rand.Intn(len(scs))
	sc := scs[index]

	// Safe return by copying the struct before taking its address
	copiedSC := sc
	return &copiedSC, nil
}

func getShipmentCarrierPaid(db *MYSQLStore) (*entity.ShipmentCarrier, error) {
	scs := db.cache.GetDict().ShipmentCarriers
	for _, sc := range scs {
		if sc.Allowed && !sc.Price.Equal(decimal.NewFromInt(0)) {
			return &sc, nil
		}
	}
	return nil, fmt.Errorf("no paid shipment carrier found")
}

func newOrder(ctx context.Context, db *MYSQLStore, items []entity.OrderItemInsert, promoCode string, i int) (*entity.OrderNew, *entity.ShipmentCarrier, error) {
	addr := &entity.AddressInsert{
		Street:          "123 Billing St",
		HouseNumber:     "Apt 4B",
		City:            "New York",
		State:           "NY",
		Country:         "USA",
		PostalCode:      "10001",
		ApartmentNumber: "",
	}

	buyer := &entity.BuyerInsert{
		FirstName:          fmt.Sprintf("order-%d", i),
		LastName:           "Doe",
		Email:              fmt.Sprintf("%d_test@test.com", i),
		Phone:              "1234567890",
		ReceivePromoEmails: true,
	}

	pm, err := getRandomPaymentMethod(db)
	if err != nil {
		return nil, nil, err
	}

	sc, err := getRandomShipmentCarrier(db)
	if err != nil {
		return nil, nil, err
	}

	return &entity.OrderNew{
		Items:             items,
		ShippingAddress:   addr,
		BillingAddress:    addr,
		Buyer:             buyer,
		PaymentMethodId:   pm.ID,
		ShipmentCarrierId: sc.ID,
		PromoCode:         promoCode,
	}, sc, nil

}

func TestCreateOrder(t *testing.T) {
	// Initialize the product store
	db := newTestDB(t)
	ps := db.Products()
	ctx := context.Background()

	np, err := randomProductInsert(db, 1)
	assert.NoError(t, err)

	xlSize, ok := db.cache.GetSizesByName(entity.XL)
	assert.True(t, ok)

	lSize, ok := db.cache.GetSizesByName(entity.L)
	assert.True(t, ok)

	np.SizeMeasurements = []entity.SizeWithMeasurementInsert{
		{
			ProductSize: entity.ProductSizeInsert{
				Quantity: decimal.NewFromInt(10),
				SizeID:   xlSize.ID,
			},
		},
		{
			ProductSize: entity.ProductSizeInsert{
				Quantity: decimal.NewFromInt(15),
				SizeID:   lSize.ID,
			},
		},
	}

	// Insert new product
	prd, err := ps.AddProduct(ctx, np)
	assert.NoError(t, err)

	p, err := ps.GetProductById(ctx, prd.Product.ID)
	assert.NoError(t, err)

	// order store
	os := db.Order()

	// creating new order with one product in xl size and quantity 1
	items := []entity.OrderItemInsert{
		{
			ProductID: p.Product.ID,
			Quantity:  decimal.NewFromInt32(1),
			SizeID:    xlSize.ID,
		},
	}
	// new order without promo code
	newOrder, sc, err := newOrder(ctx, db, items, "", 1)
	assert.NoError(t, err)

	order, err := os.CreateOrder(ctx, newOrder)
	assert.NoError(t, err)
	assert.True(t, order.TotalPrice.Equal(p.Product.Price.Add(sc.ShipmentCarrierInsert.Price)))

	// promo free shipping

	err = db.Promo().AddPromo(ctx, &entity.PromoCodeInsert{
		Code:         "freeShip",
		FreeShipping: true,
		Discount:     decimal.NewFromInt(0),
		Expiration:   time.Now().Add(time.Hour * 24),
		Allowed:      true,
	})
	assert.NoError(t, err)

	newTotal, err := os.ApplyPromoCode(ctx, order.ID, "freeShip")
	assert.NoError(t, err)
	assert.True(t, newTotal.Equal(p.Product.Price), newTotal.String())

	// promo 10% off + free shipping

	err = db.Promo().AddPromo(ctx, &entity.PromoCodeInsert{
		Code:         "freeShip10off",
		FreeShipping: true,
		Discount:     decimal.NewFromInt(10),
		Expiration:   time.Now().Add(time.Hour * 24),
		Allowed:      true,
	})
	assert.NoError(t, err)

	newTotal, err = os.ApplyPromoCode(ctx, order.ID, "freeShip10off")
	assert.NoError(t, err)
	assert.True(t, newTotal.Equal(p.Product.Price.Mul(decimal.NewFromFloat(0.9))))

	// new order items with one product size and quantity 2

	_, err = os.UpdateOrderItems(ctx, order.ID, []entity.OrderItemInsert{
		{
			ProductID: p.Product.ID,
			Quantity:  decimal.NewFromInt32(2),
			SizeID:    lSize.ID,
		},
	})
	assert.NoError(t, err)

	orderFull, err := os.GetOrderById(ctx, order.ID)
	assert.NoError(t, err)

	assert.Equal(t, 1, len(orderFull.OrderItems))
	assert.Equal(t, 2, int(orderFull.OrderItems[0].Quantity.IntPart()))
	assert.True(t, orderFull.TotalPrice.Equal(p.Product.Price.Mul(decimal.NewFromFloat(0.9)).Mul(decimal.NewFromInt32(2))))

	// new order items with one product size but passed as two separate items

	_, err = os.UpdateOrderItems(ctx, order.ID, []entity.OrderItemInsert{
		{
			ProductID: p.Product.ID,
			Quantity:  decimal.NewFromInt32(2),
			SizeID:    lSize.ID,
		},
		{
			ProductID: p.Product.ID,
			Quantity:  decimal.NewFromInt32(2),
			SizeID:    lSize.ID,
		},
	})
	assert.NoError(t, err)

	orderFull, err = os.GetOrderById(ctx, order.ID)
	assert.NoError(t, err)

	assert.Equal(t, 1, len(orderFull.OrderItems))
	assert.Equal(t, 4, int(orderFull.OrderItems[0].Quantity.IntPart()))
	assert.True(t, orderFull.TotalPrice.Equal(p.Product.Price.Mul(decimal.NewFromFloat(0.9)).Mul(decimal.NewFromInt32(4))))

	// new order items with two product sizes

	_, err = os.UpdateOrderItems(ctx, order.ID, []entity.OrderItemInsert{
		{
			ProductID: p.Product.ID,
			Quantity:  decimal.NewFromInt32(2),
			SizeID:    lSize.ID,
		},
		{
			ProductID: p.Product.ID,
			Quantity:  decimal.NewFromInt32(2),
			SizeID:    xlSize.ID,
		},
	})

	assert.NoError(t, err)

	orderFull, err = os.GetOrderById(ctx, order.ID)
	assert.NoError(t, err)

	assert.Equal(t, 2, len(orderFull.OrderItems))
	assert.Equal(t, 2, int(orderFull.OrderItems[0].Quantity.IntPart()))
	assert.Equal(t, 2, int(orderFull.OrderItems[1].Quantity.IntPart()))
	assert.True(t, orderFull.TotalPrice.Equal(p.Product.Price.Mul(decimal.NewFromFloat(0.9)).Mul(decimal.NewFromInt32(4))))

	// new order items with two product sizes with one size quantity 0

	_, err = os.UpdateOrderItems(ctx, order.ID, []entity.OrderItemInsert{
		{
			ProductID: p.Product.ID,
			Quantity:  decimal.NewFromInt32(2),
			SizeID:    lSize.ID,
		},
		{
			ProductID: p.Product.ID,
			Quantity:  decimal.NewFromInt32(0),
			SizeID:    xlSize.ID,
		},
	})

	assert.NoError(t, err)

	orderFull, err = os.GetOrderById(ctx, order.ID)
	assert.NoError(t, err)

	assert.Equal(t, 1, len(orderFull.OrderItems))
	assert.Equal(t, 2, int(orderFull.OrderItems[0].Quantity.IntPart()))
	assert.True(t, orderFull.TotalPrice.Equal(p.Product.Price.Mul(decimal.NewFromFloat(0.9)).Mul(decimal.NewFromInt32(2))))

	// update shipment carrier to paid, disable promo check
	// if amount calculated correctly

	paidSc, err := getShipmentCarrierPaid(db)
	assert.NoError(t, err)

	_, err = os.UpdateOrderShippingCarrier(ctx, order.ID, paidSc.ID)
	assert.NoError(t, err)

	err = db.Promo().AddPromo(ctx, &entity.PromoCodeInsert{
		Code:         "noPromo",
		FreeShipping: false,
		Discount:     decimal.NewFromInt(0),
		Expiration:   time.Now().Add(time.Hour * 24),
		Allowed:      true,
	})

	assert.NoError(t, err)

	newTotal, err = os.ApplyPromoCode(ctx, order.ID, "noPromo")
	assert.NoError(t, err)
	assert.True(t, newTotal.Equal(p.Product.Price.Mul(decimal.NewFromInt32(2)).Add(paidSc.ShipmentCarrierInsert.Price)))

	// new order items with two product sizes with both sizes quantity 0
	// to trigger cancellation

	_, err = os.UpdateOrderItems(ctx, order.ID, []entity.OrderItemInsert{
		{
			ProductID: p.Product.ID,
			Quantity:  decimal.NewFromInt32(0),
			SizeID:    lSize.ID,
		},
		{
			ProductID: p.Product.ID,
			Quantity:  decimal.NewFromInt32(0),
			SizeID:    xlSize.ID,
		},
	})

	assert.NoError(t, err)

	orderFull, err = os.GetOrderById(ctx, order.ID)
	assert.NoError(t, err)

	assert.True(t, orderFull.OrderStatus.Name == entity.Cancelled)

	// new order items trigger error because of status cancelled

	_, err = os.UpdateOrderItems(ctx, order.ID, []entity.OrderItemInsert{
		{
			ProductID: p.Product.ID,
			Quantity:  decimal.NewFromInt32(0),
			SizeID:    lSize.ID,
		},
	})
	assert.Error(t, err)
}

func TestPurchase(t *testing.T) {
	// Initialize the product store
	db := newTestDB(t)
	ps := db.Products()
	ctx := context.Background()

	np, err := randomProductInsert(db, 1)
	assert.NoError(t, err)

	xlSize, ok := db.cache.GetSizesByName(entity.XL)
	assert.True(t, ok)

	lSize, ok := db.cache.GetSizesByName(entity.L)
	assert.True(t, ok)

	np.SizeMeasurements = []entity.SizeWithMeasurementInsert{
		{
			ProductSize: entity.ProductSizeInsert{
				Quantity: decimal.NewFromInt(1),
				SizeID:   xlSize.ID,
			},
		},
		{
			ProductSize: entity.ProductSizeInsert{
				Quantity: decimal.NewFromInt(1),
				SizeID:   lSize.ID,
			},
		},
	}

	// Insert new product
	prd, err := ps.AddProduct(ctx, np)
	assert.NoError(t, err)

	p, err := ps.GetProductById(ctx, prd.Product.ID)
	assert.NoError(t, err)

	// order store
	os := db.Order()

	// creating new order with one product in xl size and quantity 1
	itemsXL := []entity.OrderItemInsert{
		{
			ProductID: p.Product.ID,
			Quantity:  decimal.NewFromInt32(1),
			SizeID:    xlSize.ID,
		},
	}
	itemsL := []entity.OrderItemInsert{
		{
			ProductID: p.Product.ID,
			Quantity:  decimal.NewFromInt32(1),
			SizeID:    lSize.ID,
		},
	}

	newOrderXL, scXL, err := newOrder(ctx, db, itemsXL, "", 1)
	assert.NoError(t, err)

	newOrderL, scL, err := newOrder(ctx, db, itemsL, "", 1)
	assert.NoError(t, err)

	orderXL, err := os.CreateOrder(ctx, newOrderXL)
	assert.NoError(t, err)
	assert.True(t, orderXL.TotalPrice.Equal(p.Product.Price.Add(scXL.ShipmentCarrierInsert.Price)))

	statusPlaced, ok := db.cache.GetOrderStatusByName(entity.Placed)
	assert.True(t, ok)
	assert.Equal(t, orderXL.OrderStatusID, statusPlaced.ID)

	orderL, err := os.CreateOrder(ctx, newOrderL)
	assert.NoError(t, err)
	assert.True(t, orderL.TotalPrice.Equal(p.Product.Price.Add(scL.ShipmentCarrierInsert.Price)))

	// getting product by id to check if quantity is not updated

	p, err = ps.GetProductById(ctx, prd.Product.ID)
	assert.NoError(t, err)
	assert.True(t, len(p.Sizes) == 2)
	assert.True(t, p.Sizes[0].Quantity.Equal(decimal.NewFromInt(1)))
	assert.True(t, p.Sizes[0].Quantity.Equal(decimal.NewFromInt(1)))

	// purchase order with xl size

	pi := &entity.PaymentInsert{
		TransactionID: sql.NullString{
			String: "1234567890",
			Valid:  true,
		},
		TransactionAmount: orderXL.TotalPrice,
		Payer: sql.NullString{
			String: "payer",
			Valid:  true,
		},
		Payee: sql.NullString{
			String: "payee",
			Valid:  true,
		},
		IsTransactionDone: true,
	}

	pmXL, err := getRandomPaymentMethod(db)
	assert.NoError(t, err)

	pi.PaymentMethodID = pmXL.ID
	pi.TransactionAmount = orderXL.TotalPrice

	err = os.OrderPaymentDone(ctx, orderXL.ID, pi)
	assert.NoError(t, err)

	// purchase order with l size

	pmL, err := getRandomPaymentMethod(db)
	assert.NoError(t, err)

	pi.PaymentMethodID = pmL.ID
	pi.TransactionAmount = orderL.TotalPrice

	err = os.OrderPaymentDone(ctx, orderL.ID, pi)
	assert.NoError(t, err)

	// now make sure that orders has status confirmed

	// for xl
	statusConfirmed, ok := db.cache.GetOrderStatusByName(entity.Confirmed)
	assert.True(t, ok)

	orderFullXL, err := os.GetOrderById(ctx, orderXL.ID)
	assert.NoError(t, err)

	assert.Equal(t, orderFullXL.OrderStatus.ID, statusConfirmed.ID)

	// for l
	orderFullL, err := os.GetOrderById(ctx, orderL.ID)
	assert.NoError(t, err)

	assert.Equal(t, orderFullL.OrderStatus.ID, statusConfirmed.ID)

	// than make sure that product quantity is updated

	p, err = ps.GetProductById(ctx, prd.Product.ID)
	assert.NoError(t, err)

	assert.True(t, len(p.Sizes) == 2)
	assert.True(t, p.Sizes[0].Quantity.Equal(decimal.NewFromInt(0)))
	assert.True(t, p.Sizes[1].Quantity.Equal(decimal.NewFromInt(0)))

	// try to create order with out of stock item to trigger error

	itemsOutOfStock := []entity.OrderItemInsert{
		{
			ProductID: p.Product.ID,
			Quantity:  decimal.NewFromInt32(1),
			SizeID:    lSize.ID,
		},
	}

	newOrderOutOfStock, _, err := newOrder(ctx, db, itemsOutOfStock, "", 1)
	assert.NoError(t, err)

	_, err = os.CreateOrder(ctx, newOrderOutOfStock)
	assert.Error(t, err)

}

func TestOrderOutOfStock(t *testing.T) {
	// Initialize the product store
	db := newTestDB(t)
	ps := db.Products()
	ctx := context.Background()

	np, err := randomProductInsert(db, 1)
	assert.NoError(t, err)

	xlSize, ok := db.cache.GetSizesByName(entity.XL)
	assert.True(t, ok)

	lSize, ok := db.cache.GetSizesByName(entity.L)
	assert.True(t, ok)

	np.SizeMeasurements = []entity.SizeWithMeasurementInsert{
		{
			ProductSize: entity.ProductSizeInsert{
				Quantity: decimal.NewFromInt(1),
				SizeID:   xlSize.ID,
			},
		},
		{
			ProductSize: entity.ProductSizeInsert{
				Quantity: decimal.NewFromInt(1),
				SizeID:   lSize.ID,
			},
		},
	}

	// Insert new product
	prd, err := ps.AddProduct(ctx, np)
	assert.NoError(t, err)

	p, err := ps.GetProductById(ctx, prd.Product.ID)
	assert.NoError(t, err)

	// order store
	os := db.Order()

	// creating new order with one product in xl size and quantity 1
	items := []entity.OrderItemInsert{
		{
			ProductID: p.Product.ID,
			Quantity:  decimal.NewFromInt32(1),
			SizeID:    xlSize.ID,
		},
	}

	itemsToClean := []entity.OrderItemInsert{
		{
			ProductID: p.Product.ID,
			Quantity:  decimal.NewFromInt32(1),
			SizeID:    xlSize.ID,
		},
		{
			ProductID: p.Product.ID,
			Quantity:  decimal.NewFromInt32(1),
			SizeID:    lSize.ID,
		},
	}

	// ok order with would be fulfilled

	newOrderOK, scOK, err := newOrder(ctx, db, items, "", 1)
	assert.NoError(t, err)

	orderOk, err := os.CreateOrder(ctx, newOrderOK)
	assert.NoError(t, err)
	assert.True(t, orderOk.TotalPrice.Equal(p.Product.Price.Add(scOK.ShipmentCarrierInsert.Price)))

	// bad order with would not be fulfilled because of out of stock
	newOrderBad, scBad, err := newOrder(ctx, db, items, "", 1)
	assert.NoError(t, err)

	orderBad, err := os.CreateOrder(ctx, newOrderBad)
	assert.NoError(t, err)
	assert.True(t, orderBad.TotalPrice.Equal(p.Product.Price.Add(scBad.ShipmentCarrierInsert.Price)))

	// order to clean up
	newOrderToClean, scClean, err := newOrder(ctx, db, itemsToClean, "", 1)
	assert.NoError(t, err)

	orderToClean, err := os.CreateOrder(ctx, newOrderToClean)
	assert.NoError(t, err)
	assert.True(t, orderToClean.TotalPrice.Equal(p.Product.Price.Mul(decimal.NewFromFloat32(2)).Add(scClean.ShipmentCarrierInsert.Price)))

	// check that both orders has status placed
	statusPlaced, ok := db.cache.GetOrderStatusByName(entity.Placed)
	assert.True(t, ok)
	assert.Equal(t, orderOk.OrderStatusID, statusPlaced.ID)
	assert.Equal(t, orderBad.OrderStatusID, statusPlaced.ID)
	assert.Equal(t, orderToClean.OrderStatusID, statusPlaced.ID)

	// getting product by id to check if quantity is not updated
	p, err = ps.GetProductById(ctx, prd.Product.ID)
	assert.NoError(t, err)
	assert.True(t, len(p.Sizes) == 2, len(p.Sizes))
	assert.True(t, p.Sizes[0].Quantity.Equal(decimal.NewFromInt(1)))

	// purchase first order

	pi := &entity.PaymentInsert{
		TransactionID: sql.NullString{
			String: "1234567890",
			Valid:  true,
		},
		TransactionAmount: orderOk.TotalPrice,
		Payer: sql.NullString{
			String: "payer",
			Valid:  true,
		},
		Payee: sql.NullString{
			String: "payee",
			Valid:  true,
		},
		IsTransactionDone: true,
	}

	pm, err := getRandomPaymentMethod(db)
	assert.NoError(t, err)

	pi.PaymentMethodID = pm.ID
	pi.TransactionAmount = orderOk.TotalPrice

	// try to pay for ok order
	err = os.OrderPaymentDone(ctx, orderOk.ID, pi)
	assert.NoError(t, err)

	// try to pay for bad order where product is out of stock
	// must trigger error and change status to cancelled
	// cause every order item is out of stock
	pi.TransactionAmount = orderBad.TotalPrice

	err = os.OrderPaymentDone(ctx, orderBad.ID, pi)
	assert.Error(t, err)

	// try to pay for bad order where product is out of stock
	// must trigger error and clean up order items
	// i.e remove order items with quantity 0

	pi.TransactionAmount = orderToClean.TotalPrice
	err = os.OrderPaymentDone(ctx, orderToClean.ID, pi)
	assert.Error(t, err)

	// on second try order would be cleaned up and can be paid

	err = os.OrderPaymentDone(ctx, orderToClean.ID, pi)
	assert.NoError(t, err)

	// orders by status

	// one cancelled - bad order
	orders, err := os.GetOrdersByStatus(ctx, entity.Cancelled)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(orders))

	email := orders[0].Buyer.Email

	// two confirmed - ok order and order to clean up
	orders, err = os.GetOrdersByStatus(ctx, entity.Confirmed)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(orders))

	// by email 3 orders - ok order, order to clean up and bad order

	orders, err = os.GetOrdersByEmail(ctx, email)
	assert.NoError(t, err)
	assert.Equal(t, 3, len(orders))
}

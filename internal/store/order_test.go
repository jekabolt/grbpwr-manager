package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type OrderTestSuite struct {
	suite.Suite
	store   *MYSQLStore
	ctx     context.Context
	product entity.Product
	cleanup func()
}

func (s *OrderTestSuite) SetupTest() {
	store, ctx := setupTestOrder(s.T())
	s.store = store
	s.ctx = ctx

	// Clean up any existing test data
	err := cleanupOrders(ctx, store)
	require.NoError(s.T(), err)
	err = cleanupProducts(ctx, store)
	require.NoError(s.T(), err)
	err = cleanupPayments(ctx, store)
	require.NoError(s.T(), err)

	s.product = createTestProduct(s.T(), store)
	s.cleanup = func() {
		err := cleanupOrders(ctx, store)
		require.NoError(s.T(), err)
		err = cleanupProducts(ctx, store)
		require.NoError(s.T(), err)
		err = cleanupPayments(ctx, store)
		require.NoError(s.T(), err)
		store.Close()
	}
}

func (s *OrderTestSuite) TearDownTest() {
	if s.cleanup != nil {
		s.cleanup()
	}
}

func setupTestOrder(t *testing.T) (*MYSQLStore, context.Context) {
	ctx := context.Background()
	store, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	// Create test media for hero
	mediaIds, err := createTestMedia(ctx, store, 4)
	require.NoError(t, err)

	// Create hero data
	heroInsert := entity.HeroFullInsert{
		Entities: []entity.HeroEntityInsert{
			{
				Type: entity.HeroTypeMain,
				Main: entity.HeroMainInsert{
					Single: entity.HeroSingleInsert{
						MediaPortraitId:  mediaIds[0],
						MediaLandscapeId: mediaIds[1],
						ExploreLink:      "https://example.com",
						ExploreText:      "Explore Now",
						Headline:         "Test Headline",
					},
					Tag:         "test-tag",
					Description: "Test Description",
				},
			},
			{
				Type: entity.HeroTypeSingle,
				Single: entity.HeroSingleInsert{
					MediaPortraitId:  mediaIds[2],
					MediaLandscapeId: mediaIds[3],
					Headline:         "Single Test",
					ExploreLink:      "https://single.com",
					ExploreText:      "View Single",
				},
			},
		},
		NavFeatured: entity.NavFeaturedInsert{
			Men: entity.NavFeaturedEntityInsert{
				MediaId:           mediaIds[0],
				ExploreText:       "Men's Collection",
				FeaturedTag:       "men",
				FeaturedArchiveId: 1,
			},
			Women: entity.NavFeaturedEntityInsert{
				MediaId:           mediaIds[1],
				ExploreText:       "Women's Collection",
				FeaturedTag:       "women",
				FeaturedArchiveId: 2,
			},
		},
	}

	err = store.Hero().SetHero(ctx, heroInsert)
	require.NoError(t, err)

	// Initialize cache with dictionary info
	di, err := store.GetDictionaryInfo(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, di.OrderStatuses, "order statuses should not be empty")
	require.NotEmpty(t, di.PaymentMethods, "payment methods should not be empty")

	hf, err := store.Hero().GetHero(ctx)
	require.NoError(t, err)

	// Ensure order statuses are properly initialized in the database
	for _, status := range di.OrderStatuses {
		if status.Name == entity.AwaitingPayment {
			require.NotZero(t, status.Id, "awaiting payment status ID should not be zero")
		}
		if status.Name == entity.Shipped {
			require.NotZero(t, status.Id, "shipped status ID should not be zero")
		}
	}

	// Ensure payment methods are properly initialized in the database
	for _, pm := range di.PaymentMethods {
		if pm.Name == entity.CARD_TEST {
			require.NotZero(t, pm.Id, "card test payment method ID should not be zero")
			require.True(t, pm.Allowed, "card test payment method should be allowed")
		}
	}

	err = cache.InitConsts(ctx, di, hf)
	require.NoError(t, err)

	// Verify cache initialization
	awaitingPaymentStatus, ok := cache.GetOrderStatusByName(entity.AwaitingPayment)
	require.True(t, ok, "awaiting payment status should be initialized")
	require.NotZero(t, awaitingPaymentStatus.Status.Id, "awaiting payment status ID should not be zero")

	cardTestMethod := cache.PaymentMethodCardTest
	require.NotZero(t, cardTestMethod.Method.Id, "card test payment method ID should not be zero")
	require.True(t, cardTestMethod.Method.Allowed, "card test payment method should be allowed")

	// Print available sizes for debugging
	type Size struct {
		Id   int    `db:"id"`
		Name string `db:"name"`
	}
	sizes, err := QueryListNamed[Size](ctx, store.DB(), "SELECT id, name FROM size", nil)
	require.NoError(t, err)
	require.NotEmpty(t, sizes, "no sizes found in database")

	return store, ctx
}

func createTestProduct(t *testing.T, store *MYSQLStore) entity.Product {
	// Create media first
	ctx := context.Background()
	media := entity.MediaItem{
		FullSizeMediaURL:   "test.jpg",
		FullSizeWidth:      1000,
		FullSizeHeight:     1000,
		ThumbnailMediaURL:  "test_thumb.jpg",
		ThumbnailWidth:     100,
		ThumbnailHeight:    100,
		CompressedMediaURL: "test_comp.jpg",
		CompressedWidth:    500,
		CompressedHeight:   500,
		BlurHash:           sql.NullString{String: "test-blur-hash", Valid: true},
	}
	mediaID, err := store.Media().AddMedia(ctx, &media)
	require.NoError(t, err)

	// Get valid size ID from cache
	di, err := store.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, di.Sizes, "no sizes found in dictionary")

	// Find size "m" for testing
	var validSizeID int32
	for _, size := range di.Sizes {
		if size.Name == "m" {
			validSizeID = int32(size.Id)
			break
		}
	}
	require.NotZero(t, validSizeID, "size 'm' not found in dictionary")

	// Generate unique SKU using timestamp
	sku := "TEST-SKU-" + time.Now().Format("20060102150405")

	product := entity.Product{
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		ProductDisplay: entity.ProductDisplay{
			ProductBody: entity.ProductBody{
				Preorder:           sql.NullTime{Time: time.Now(), Valid: true},
				Name:               "Test Product",
				Brand:              "Test Brand",
				SKU:                sku,
				Color:              "Black",
				ColorHex:           "#000000",
				CountryOfOrigin:    "Test Country",
				Price:              decimal.NewFromFloat(100.00),
				SalePercentage:     decimal.NullDecimal{Decimal: decimal.Zero, Valid: true},
				TopCategoryId:      1,
				SubCategoryId:      sql.NullInt32{Int32: 1, Valid: true},
				TypeId:             sql.NullInt32{Int32: 1, Valid: true},
				ModelWearsHeightCm: sql.NullInt32{Int32: 180, Valid: true},
				ModelWearsSizeId:   sql.NullInt32{Int32: validSizeID, Valid: true},
				Description:        "Test Description",
				Hidden:             sql.NullBool{Bool: false, Valid: true},
				TargetGender:       entity.Unisex,
				CareInstructions:   sql.NullString{String: "MWN,GW,BA", Valid: true},
				Composition:        sql.NullString{String: "COTTON:100", Valid: true},
			},
			MediaFull: entity.MediaFull{
				Id:        mediaID,
				CreatedAt: time.Now(),
				MediaItem: media,
			},
			ThumbnailMediaID: mediaID,
		},
	}

	// Create product in the database
	productID, err := store.Products().AddProduct(ctx, &entity.ProductNew{
		Product: &entity.ProductInsert{
			ProductBody:      product.ProductBody,
			ThumbnailMediaID: product.ThumbnailMediaID,
		},
		SizeMeasurements: []entity.SizeWithMeasurementInsert{
			{
				ProductSize: entity.ProductSizeInsert{
					Quantity: decimal.NewFromInt(10),
					SizeId:   int(validSizeID),
				},
			},
		},
		MediaIds: []int{mediaID},
		Tags:     []entity.ProductTagInsert{{Tag: "test"}},
	})
	require.NoError(t, err)

	// Insert product size with quantity
	err = store.Products().UpdateProductSizeStock(ctx, productID, int(validSizeID), 10)
	require.NoError(t, err)

	product.Id = productID
	return product
}

func createTestOrderNew(t *testing.T, store *MYSQLStore, product entity.Product) *entity.OrderNew {
	// Get valid size ID from cache
	ctx := context.Background()
	di, err := store.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, di.Sizes, "no sizes found in dictionary")

	// Find size "m" for testing
	var validSizeID int
	for _, size := range di.Sizes {
		if size.Name == "m" {
			validSizeID = size.Id
			break
		}
	}
	require.NotZero(t, validSizeID, "size 'm' not found in dictionary")

	return &entity.OrderNew{
		Items: []entity.OrderItemInsert{
			{
				ProductId: product.Id,
				Quantity:  decimal.NewFromInt(1),
				SizeId:    validSizeID,
			},
		},
		Currency: "USD",
		ShippingAddress: &entity.AddressInsert{
			Country:        "Test Country",
			State:          sql.NullString{String: "Test State", Valid: true},
			City:           "Test City",
			AddressLineOne: "123 Test St",
			PostalCode:     "12345",
		},
		BillingAddress: &entity.AddressInsert{
			Country:        "Test Country",
			State:          sql.NullString{String: "Test State", Valid: true},
			City:           "Test City",
			AddressLineOne: "123 Test St",
			PostalCode:     "12345",
		},
		Buyer: &entity.BuyerInsert{
			FirstName: "Test",
			LastName:  "User",
			Email:     "test@example.com",
			Phone:     "1234567890",
		},
		PaymentMethod:     entity.CARD_TEST,
		ShipmentCarrierId: 1,
		PromoCode:         "",
	}
}

func TestOrderLifecycle(t *testing.T) {
	store, ctx := setupTestOrder(t)
	defer store.Close()

	// Clean up any existing test data
	err := cleanupOrders(ctx, store)
	require.NoError(t, err)
	err = cleanupProducts(ctx, store)
	require.NoError(t, err)
	err = cleanupPayments(ctx, store)
	require.NoError(t, err)

	product := createTestProduct(t, store)
	orderNew := createTestOrderNew(t, store, product)

	t.Run("ValidateOrderItemsInsert", func(t *testing.T) {
		// Test with invalid quantity
		invalidQuantityOrder := createTestOrderNew(t, store, product)
		invalidQuantityOrder.Items[0].Quantity = decimal.NewFromInt(-1)
		_, err := store.Order().ValidateOrderItemsInsert(ctx, invalidQuantityOrder.Items, invalidQuantityOrder.Currency)
		require.Error(t, err)
		require.Contains(t, err.Error(), "quantity for product ID")
		require.Contains(t, err.Error(), "is not positive")

		// Test with non-existent product
		nonExistentProductOrder := createTestOrderNew(t, store, product)
		nonExistentProductOrder.Items[0].ProductId = 99999
		_, err = store.Order().ValidateOrderItemsInsert(ctx, nonExistentProductOrder.Items, nonExistentProductOrder.Currency)
		require.Error(t, err)
		require.Contains(t, err.Error(), "error while validating order items")
		require.Contains(t, err.Error(), "no valid order items to insert")

		// Test with non-existent size
		nonExistentSizeOrder := createTestOrderNew(t, store, product)
		nonExistentSizeOrder.Items[0].SizeId = 99999
		_, err = store.Order().ValidateOrderItemsInsert(ctx, nonExistentSizeOrder.Items, nonExistentSizeOrder.Currency)
		require.Error(t, err)
		require.Contains(t, err.Error(), "error while validating order items")
		require.Contains(t, err.Error(), "no valid order items to insert")

		// Test with quantity exceeding stock
		exceedingStockOrder := createTestOrderNew(t, store, product)
		exceedingStockOrder.Items[0].Quantity = decimal.NewFromInt(100)
		adjustedItems, err := store.Order().ValidateOrderItemsInsert(ctx, exceedingStockOrder.Items, exceedingStockOrder.Currency)
		require.NoError(t, err)
		require.NotNil(t, adjustedItems)
		require.NotEmpty(t, adjustedItems.ValidItems)
		require.Equal(t, decimal.NewFromInt(3), adjustedItems.ValidItems[0].Quantity) // Adjusted to maxOrderItemPerSize from cache
		require.False(t, adjustedItems.Subtotal.IsZero())
		require.NotEmpty(t, adjustedItems.ItemAdjustments, "should have adjustments when quantity exceeds stock or max")

		// Test valid order items
		validOrder := createTestOrderNew(t, store, product)
		validItems, err := store.Order().ValidateOrderItemsInsert(ctx, validOrder.Items, validOrder.Currency)
		require.NoError(t, err)
		require.NotNil(t, validItems)
		require.NotEmpty(t, validItems.ValidItems)
		require.False(t, validItems.Subtotal.IsZero())
	})

	// Normal payment flow
	t.Run("Normal_Payment_Flow", func(t *testing.T) {
		// Create and validate order
		order, _, err := store.Order().CreateOrder(ctx, orderNew, false, time.Now().Add(time.Hour))
		require.NoError(t, err)
		require.NotNil(t, order)

		// Check initial status is Placed
		status, ok := cache.GetOrderStatusById(order.OrderStatusId)
		require.True(t, ok)
		require.Equal(t, entity.Placed, status.Status.Name)

		// Insert fiat invoice and check status change to AwaitingPayment
		orderFull, err := store.Order().InsertFiatInvoice(ctx, order.UUID, "test-client-secret", entity.PaymentMethod{
			Id:      cache.PaymentMethodCardTest.Method.Id,
			Name:    entity.CARD_TEST,
			Allowed: true,
		})
		require.NoError(t, err)
		status, ok = cache.GetOrderStatusById(orderFull.Order.OrderStatusId)
		require.True(t, ok)
		require.Equal(t, entity.AwaitingPayment, status.Status.Name)

		// Complete payment and check status change to Confirmed
		payment := &entity.Payment{
			PaymentInsert: entity.PaymentInsert{
				OrderId:                          order.Id,
				PaymentMethodID:                  cache.PaymentMethodCardTest.Method.Id,
				TransactionID:                    sql.NullString{String: "tx_123_normal", Valid: true},
				TransactionAmount:                decimal.NewFromFloat(100.00),
				TransactionAmountPaymentCurrency: decimal.NewFromFloat(100.00),
				IsTransactionDone:                true,
			},
		}
		_, err = store.Order().OrderPaymentDone(ctx, order.UUID, payment)
		require.NoError(t, err)

		orderFull, err = store.Order().GetOrderFullByUUID(ctx, order.UUID)
		require.NoError(t, err)
		status, ok = cache.GetOrderStatusById(orderFull.Order.OrderStatusId)
		require.True(t, ok)
		require.Equal(t, entity.Confirmed, status.Status.Name)
	})

	t.Run("Payment_Expiration_Flow", func(t *testing.T) {
		// Create order with short expiration
		order, _, err := store.Order().CreateOrder(ctx, orderNew, false, time.Now().Add(time.Second))
		require.NoError(t, err)

		// Insert fiat invoice
		orderFull, err := store.Order().InsertFiatInvoice(ctx, order.UUID, "test-client-secret", entity.PaymentMethod{
			Id:      cache.PaymentMethodCardTest.Method.Id,
			Name:    entity.CARD_TEST,
			Allowed: true,
		})
		require.NoError(t, err)
		status, ok := cache.GetOrderStatusById(orderFull.Order.OrderStatusId)
		require.True(t, ok)
		require.Equal(t, entity.AwaitingPayment, status.Status.Name)

		// Wait for expiration
		time.Sleep(2 * time.Second)

		// Expire the payment
		payment, err := store.Order().ExpireOrderPayment(ctx, order.UUID)
		require.NoError(t, err)
		require.True(t, payment.ExpiredAt.Valid)

		orderFull, err = store.Order().GetOrderFullByUUID(ctx, order.UUID)
		require.NoError(t, err)
		status, ok = cache.GetOrderStatusById(orderFull.Order.OrderStatusId)
		require.True(t, ok)
		require.Equal(t, entity.Cancelled, status.Status.Name)
	})

	t.Run("Shipping_Flow", func(t *testing.T) {
		// Create and pay for order
		order, _, err := store.Order().CreateOrder(ctx, orderNew, false, time.Now().Add(time.Hour))
		require.NoError(t, err)

		// Insert fiat invoice
		_, err = store.Order().InsertFiatInvoice(ctx, order.UUID, "test-client-secret", entity.PaymentMethod{
			Id:      cache.PaymentMethodCardTest.Method.Id,
			Name:    entity.CARD_TEST,
			Allowed: true,
		})
		require.NoError(t, err)

		payment := &entity.Payment{
			PaymentInsert: entity.PaymentInsert{
				OrderId:                          order.Id,
				PaymentMethodID:                  cache.PaymentMethodCardTest.Method.Id,
				TransactionID:                    sql.NullString{String: "tx_123_shipping", Valid: true},
				TransactionAmount:                decimal.NewFromFloat(100.00),
				TransactionAmountPaymentCurrency: decimal.NewFromFloat(100.00),
				IsTransactionDone:                true,
			},
		}
		_, err = store.Order().OrderPaymentDone(ctx, order.UUID, payment)
		require.NoError(t, err)

		// Set tracking number and verify status change to Shipped
		orderFull, err := store.Order().SetTrackingNumber(ctx, order.UUID, "TRACK123")
		require.NoError(t, err)
		require.Equal(t, "TRACK123", orderFull.Shipment.TrackingCode.String)

		status, ok := cache.GetOrderStatusById(orderFull.Order.OrderStatusId)
		require.True(t, ok)
		require.Equal(t, entity.Shipped, status.Status.Name)
	})

	t.Run("Cancellation_Flow", func(t *testing.T) {
		// Create order
		order, _, err := store.Order().CreateOrder(ctx, orderNew, false, time.Now().Add(time.Hour))
		require.NoError(t, err)

		// Try to cancel order in Placed status
		err = store.Order().CancelOrder(ctx, order.UUID)
		require.NoError(t, err)

		orderFull, err := store.Order().GetOrderFullByUUID(ctx, order.UUID)
		require.NoError(t, err)
		status, ok := cache.GetOrderStatusById(orderFull.Order.OrderStatusId)
		require.True(t, ok)
		require.Equal(t, entity.Cancelled, status.Status.Name)
	})

	t.Run("Refund_Flow", func(t *testing.T) {
		// Create and pay for order
		order, _, err := store.Order().CreateOrder(ctx, orderNew, false, time.Now().Add(time.Hour))
		require.NoError(t, err)

		// Insert fiat invoice
		_, err = store.Order().InsertFiatInvoice(ctx, order.UUID, "test-client-secret", entity.PaymentMethod{
			Id:      cache.PaymentMethodCardTest.Method.Id,
			Name:    entity.CARD_TEST,
			Allowed: true,
		})
		require.NoError(t, err)

		payment := &entity.Payment{
			PaymentInsert: entity.PaymentInsert{
				OrderId:                          order.Id,
				PaymentMethodID:                  cache.PaymentMethodCardTest.Method.Id,
				TransactionID:                    sql.NullString{String: "tx_123_refund", Valid: true},
				TransactionAmount:                decimal.NewFromFloat(100.00),
				TransactionAmountPaymentCurrency: decimal.NewFromFloat(100.00),
				IsTransactionDone:                true,
			},
		}
		_, err = store.Order().OrderPaymentDone(ctx, order.UUID, payment)
		require.NoError(t, err)

		// Set tracking number to move to Shipped status
		_, err = store.Order().SetTrackingNumber(ctx, order.UUID, "TRACK123")
		require.NoError(t, err)

		// Mark as delivered
		err = store.Order().DeliveredOrder(ctx, order.UUID)
		require.NoError(t, err)

		// User cancels from Delivered -> PendingReturn (parcel return requested)
		_, err = store.Order().CancelOrderByUser(ctx, order.UUID, orderNew.Buyer.Email, "changed mind")
		require.NoError(t, err)

		// Admin refunds (full refund, empty orderItemIDs)
		err = store.Order().RefundOrder(ctx, order.UUID, nil, "")
		require.NoError(t, err)

		orderFull, err := store.Order().GetOrderFullByUUID(ctx, order.UUID)
		require.NoError(t, err)
		status, ok := cache.GetOrderStatusById(orderFull.Order.OrderStatusId)
		require.True(t, ok)
		require.Equal(t, entity.Refunded, status.Status.Name)
	})

	t.Run("Partial_Refund_Flow", func(t *testing.T) {
		// Create order with 2 items for partial refund test
		orderNewPartial := createTestOrderNew(t, store, product)
		orderNewPartial.Items = append(orderNewPartial.Items, entity.OrderItemInsert{
			ProductId: product.Id,
			Quantity:  decimal.NewFromInt(1),
			SizeId:    orderNewPartial.Items[0].SizeId,
		})
		order, _, err := store.Order().CreateOrder(ctx, orderNewPartial, false, time.Now().Add(time.Hour))
		require.NoError(t, err)

		_, err = store.Order().InsertFiatInvoice(ctx, order.UUID, "test-client-secret", entity.PaymentMethod{
			Id:      cache.PaymentMethodCardTest.Method.Id,
			Name:    entity.CARD_TEST,
			Allowed: true,
		})
		require.NoError(t, err)

		payment := &entity.Payment{
			PaymentInsert: entity.PaymentInsert{
				OrderId:                          order.Id,
				PaymentMethodID:                  cache.PaymentMethodCardTest.Method.Id,
				TransactionID:                    sql.NullString{String: "tx_123_partial", Valid: true},
				TransactionAmount:                decimal.NewFromFloat(200.00),
				TransactionAmountPaymentCurrency: decimal.NewFromFloat(200.00),
				IsTransactionDone:                true,
			},
		}
		_, err = store.Order().OrderPaymentDone(ctx, order.UUID, payment)
		require.NoError(t, err)
		_, err = store.Order().SetTrackingNumber(ctx, order.UUID, "TRACK456")
		require.NoError(t, err)
		err = store.Order().DeliveredOrder(ctx, order.UUID)
		require.NoError(t, err)

		// User cancels -> PendingReturn
		_, err = store.Order().CancelOrderByUser(ctx, order.UUID, orderNewPartial.Buyer.Email, "partial return")
		require.NoError(t, err)

		// Get order items to select one for partial refund
		orderFull, err := store.Order().GetOrderFullByUUID(ctx, order.UUID)
		require.NoError(t, err)
		require.Len(t, orderFull.OrderItems, 2)
		firstItemID := int32(orderFull.OrderItems[0].Id)

		// Admin partial refund - only first item
		err = store.Order().RefundOrder(ctx, order.UUID, []int32{firstItemID}, "")
		require.NoError(t, err)

		orderFull, err = store.Order().GetOrderFullByUUID(ctx, order.UUID)
		require.NoError(t, err)
		status, ok := cache.GetOrderStatusById(orderFull.Order.OrderStatusId)
		require.True(t, ok)
		require.Equal(t, entity.PartiallyRefunded, status.Status.Name)
	})

	t.Run("Delivery_Flow", func(t *testing.T) {
		// Create and pay for order
		order, _, err := store.Order().CreateOrder(ctx, orderNew, false, time.Now().Add(time.Hour))
		require.NoError(t, err)

		// Insert fiat invoice
		_, err = store.Order().InsertFiatInvoice(ctx, order.UUID, "test-client-secret", entity.PaymentMethod{
			Id:      cache.PaymentMethodCardTest.Method.Id,
			Name:    entity.CARD_TEST,
			Allowed: true,
		})
		require.NoError(t, err)

		payment := &entity.Payment{
			PaymentInsert: entity.PaymentInsert{
				OrderId:                          order.Id,
				PaymentMethodID:                  cache.PaymentMethodCardTest.Method.Id,
				TransactionID:                    sql.NullString{String: "tx_123_delivery", Valid: true},
				TransactionAmount:                decimal.NewFromFloat(100.00),
				TransactionAmountPaymentCurrency: decimal.NewFromFloat(100.00),
				IsTransactionDone:                true,
			},
		}
		_, err = store.Order().OrderPaymentDone(ctx, order.UUID, payment)
		require.NoError(t, err)

		// Set tracking number to move to Shipped status
		_, err = store.Order().SetTrackingNumber(ctx, order.UUID, "TRACK123")
		require.NoError(t, err)

		// Mark as delivered
		err = store.Order().DeliveredOrder(ctx, order.UUID)
		require.NoError(t, err)

		orderFull, err := store.Order().GetOrderFullByUUID(ctx, order.UUID)
		require.NoError(t, err)
		status, ok := cache.GetOrderStatusById(orderFull.Order.OrderStatusId)
		require.True(t, ok)
		require.Equal(t, entity.Delivered, status.Status.Name)
	})

	t.Run("ValidateOrderByUUID_Flow", func(t *testing.T) {
		// Create initial order
		order, _, err := store.Order().CreateOrder(ctx, orderNew, false, time.Now().Add(time.Hour))
		require.NoError(t, err)

		// Test validation when order is in Placed status
		orderFull, err := store.Order().ValidateOrderByUUID(ctx, order.UUID)
		require.NoError(t, err)
		require.NotNil(t, orderFull)
		status, ok := cache.GetOrderStatusById(orderFull.Order.OrderStatusId)
		require.True(t, ok)
		require.Equal(t, entity.Placed, status.Status.Name)

		// Move order to AwaitingPayment status
		_, err = store.Order().InsertFiatInvoice(ctx, order.UUID, "test-client-secret", entity.PaymentMethod{
			Id:      cache.PaymentMethodCardTest.Method.Id,
			Name:    entity.CARD_TEST,
			Allowed: true,
		})
		require.NoError(t, err)

		// Test validation when order is not in Placed status
		orderFull, err = store.Order().ValidateOrderByUUID(ctx, order.UUID)
		require.NoError(t, err)
		require.NotNil(t, orderFull)
		status, ok = cache.GetOrderStatusById(orderFull.Order.OrderStatusId)
		require.True(t, ok)
		require.Equal(t, entity.AwaitingPayment, status.Status.Name)

		// Create another order to test validation with stock changes
		orderNew2 := createTestOrderNew(t, store, product)
		orderNew2.Items[0].Quantity = decimal.NewFromInt(5) // Set higher quantity
		order2, _, err := store.Order().CreateOrder(ctx, orderNew2, false, time.Now().Add(time.Hour))
		require.NoError(t, err)

		// Reduce stock to force quantity adjustment during validation
		err = store.Products().UpdateProductSizeStock(ctx, product.Id, orderNew2.Items[0].SizeId, 2)
		require.NoError(t, err)

		// Test validation with stock changes
		_, err = store.Order().ValidateOrderByUUID(ctx, order2.UUID)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrOrderItemsUpdated))

		// Verify the order was updated with adjusted quantity
		updatedOrder, err := store.Order().GetOrderFullByUUID(ctx, order2.UUID)
		require.NoError(t, err)
		require.Equal(t, decimal.NewFromInt(2), updatedOrder.OrderItems[0].Quantity)
	})

	t.Run("GetOrderById_Flow", func(t *testing.T) {
		// Create and pay for order
		order, _, err := store.Order().CreateOrder(ctx, orderNew, false, time.Now().Add(time.Hour))
		require.NoError(t, err)
		require.NotNil(t, order)

		// Get order by ID
		orderFull, err := store.Order().GetOrderById(ctx, order.Id)
		require.NoError(t, err)
		require.NotNil(t, orderFull)

		// Verify order details
		require.Equal(t, order.Id, orderFull.Order.Id)
		require.Equal(t, order.UUID, orderFull.Order.UUID)
		require.Equal(t, order.TotalPrice.String(), orderFull.Order.TotalPrice.String())

		// Verify order items
		require.NotEmpty(t, orderFull.OrderItems)
		require.Equal(t, orderNew.Items[0].ProductId, orderFull.OrderItems[0].ProductId)
		require.Equal(t, orderNew.Items[0].Quantity.String(), orderFull.OrderItems[0].Quantity.String())
		require.Equal(t, orderNew.Items[0].SizeId, orderFull.OrderItems[0].SizeId)

		// Verify buyer details
		require.NotNil(t, orderFull.Buyer)
		require.Equal(t, orderNew.Buyer.FirstName, orderFull.Buyer.FirstName)
		require.Equal(t, orderNew.Buyer.LastName, orderFull.Buyer.LastName)
		require.Equal(t, orderNew.Buyer.Email, orderFull.Buyer.Email)
		require.Equal(t, orderNew.Buyer.Phone, orderFull.Buyer.Phone)

		// Verify shipping address
		require.NotNil(t, orderFull.Shipping)
		require.Equal(t, orderNew.ShippingAddress.Country, orderFull.Shipping.Country)
		require.Equal(t, orderNew.ShippingAddress.City, orderFull.Shipping.City)
		require.Equal(t, orderNew.ShippingAddress.AddressLineOne, orderFull.Shipping.AddressLineOne)
		require.Equal(t, orderNew.ShippingAddress.PostalCode, orderFull.Shipping.PostalCode)

		// Test non-existent order ID
		_, err = store.Order().GetOrderById(ctx, 99999)
		require.Error(t, err)
		require.Contains(t, err.Error(), "order is not found")
	})

	t.Run("GetOrderByUUID_Flow", func(t *testing.T) {
		// Create and pay for order
		order, _, err := store.Order().CreateOrder(ctx, orderNew, false, time.Now().Add(time.Hour))
		require.NoError(t, err)
		require.NotNil(t, order)

		// Get order by UUID
		orderByUUID, err := store.Order().GetOrderByUUID(ctx, order.UUID)
		require.NoError(t, err)
		require.NotNil(t, orderByUUID)

		// Verify order details
		require.Equal(t, order.Id, orderByUUID.Id)
		require.Equal(t, order.UUID, orderByUUID.UUID)
		require.Equal(t, order.TotalPrice.String(), orderByUUID.TotalPrice.String())
		require.Equal(t, order.OrderStatusId, orderByUUID.OrderStatusId)
		require.Equal(t, order.PromoId, orderByUUID.PromoId)

		// Test non-existent order UUID
		_, err = store.Order().GetOrderByUUID(ctx, "non-existent-uuid")
		require.Error(t, err)
		require.Contains(t, err.Error(), "sql: no rows in result set")
	})

	t.Run("GetOrderFullByUUID_Flow", func(t *testing.T) {
		// Clean up any existing test data
		err := cleanupOrders(ctx, store)
		require.NoError(t, err)
		err = cleanupProducts(ctx, store)
		require.NoError(t, err)
		err = cleanupPayments(ctx, store)
		require.NoError(t, err)

		// Create test product and get its actual ID
		product := createTestProduct(t, store)
		orderNew := createTestOrderNew(t, store, product) // Create a fresh orderNew for this test

		// Create order
		order, _, err := store.Order().CreateOrder(ctx, orderNew, false, time.Now().Add(time.Hour))
		require.NoError(t, err)
		require.NotNil(t, order)

		// Get full order by UUID before adding payment
		orderFullBeforePayment, err := store.Order().GetOrderFullByUUID(ctx, order.UUID)
		require.NoError(t, err)
		require.NotNil(t, orderFullBeforePayment)
		require.Equal(t, cache.OrderStatusPlaced.Status.Id, orderFullBeforePayment.Order.OrderStatusId)
		require.NotZero(t, orderFullBeforePayment.Payment.Id)               // Payment record should exist
		require.False(t, orderFullBeforePayment.Payment.ClientSecret.Valid) // But should not have client secret yet
		require.False(t, orderFullBeforePayment.Payment.IsTransactionDone)  // And transaction should not be done

		// Insert fiat invoice to create payment record
		orderWithInvoice, err := store.Order().InsertFiatInvoice(ctx, order.UUID, "test-client-secret", entity.PaymentMethod{
			Id:      cache.PaymentMethodCardTest.Method.Id,
			Name:    entity.CARD_TEST,
			Allowed: true,
		})
		require.NoError(t, err)
		require.NotNil(t, orderWithInvoice)

		// Get full order by UUID after adding payment
		orderFull, err := store.Order().GetOrderFullByUUID(ctx, order.UUID)
		require.NoError(t, err)
		require.NotNil(t, orderFull)

		// Verify order details
		require.Equal(t, order.Id, orderFull.Order.Id)
		require.Equal(t, order.UUID, orderFull.Order.UUID)
		require.Equal(t, order.TotalPrice.String(), orderFull.Order.TotalPrice.String())
		require.Equal(t, cache.OrderStatusAwaitingPayment.Status.Id, orderFull.Order.OrderStatusId)
		require.Equal(t, order.PromoId, orderFull.Order.PromoId)

		// Verify order items
		require.NotEmpty(t, orderFull.OrderItems)
		require.Len(t, orderFull.OrderItems, 1)
		require.Equal(t, product.Id, orderFull.OrderItems[0].ProductId)
		require.Equal(t, orderNew.Items[0].SizeId, orderFull.OrderItems[0].SizeId)
		require.True(t, orderNew.Items[0].Quantity.Equal(orderFull.OrderItems[0].Quantity))
		require.Equal(t, product.Price.String(), orderFull.OrderItems[0].ProductPrice.String())

		// Verify buyer details
		require.NotNil(t, orderFull.Buyer)
		require.Equal(t, orderNew.Buyer.FirstName, orderFull.Buyer.FirstName)
		require.Equal(t, orderNew.Buyer.LastName, orderFull.Buyer.LastName)
		require.Equal(t, orderNew.Buyer.Email, orderFull.Buyer.Email)
		require.Equal(t, orderNew.Buyer.Phone, orderFull.Buyer.Phone)

		// Verify shipping address
		require.NotNil(t, orderFull.Shipping)
		require.Equal(t, orderNew.ShippingAddress.Country, orderFull.Shipping.Country)
		require.Equal(t, orderNew.ShippingAddress.City, orderFull.Shipping.City)
		require.Equal(t, orderNew.ShippingAddress.AddressLineOne, orderFull.Shipping.AddressLineOne)
		require.Equal(t, orderNew.ShippingAddress.PostalCode, orderFull.Shipping.PostalCode)

		// Verify payment details
		require.NotNil(t, orderFull.Payment)
		require.NotZero(t, orderFull.Payment.Id)
		require.Equal(t, cache.PaymentMethodCardTest.Method.Id, orderFull.Payment.PaymentMethodID)
		require.Equal(t, "test-client-secret", orderFull.Payment.ClientSecret.String)
		require.False(t, orderFull.Payment.IsTransactionDone)
		require.Empty(t, orderFull.Payment.TransactionID.String)

		// Complete the payment
		payment := &entity.Payment{
			PaymentInsert: entity.PaymentInsert{
				OrderId:                          order.Id,
				PaymentMethodID:                  cache.PaymentMethodCardTest.Method.Id,
				TransactionID:                    sql.NullString{String: "tx_123_test", Valid: true},
				TransactionAmount:                orderFull.Order.TotalPrice,
				TransactionAmountPaymentCurrency: orderFull.Order.TotalPrice,
				IsTransactionDone:                true,
			},
		}
		_, err = store.Order().OrderPaymentDone(ctx, order.UUID, payment)
		require.NoError(t, err)

		// Get full order after payment completion
		orderFullAfterPayment, err := store.Order().GetOrderFullByUUID(ctx, order.UUID)
		require.NoError(t, err)
		require.NotNil(t, orderFullAfterPayment)
		require.True(t, orderFullAfterPayment.Payment.IsTransactionDone)
		require.Equal(t, "tx_123_test", orderFullAfterPayment.Payment.TransactionID.String)
		require.Equal(t, cache.OrderStatusConfirmed.Status.Id, orderFullAfterPayment.Order.OrderStatusId)

		// Test non-existent order UUID
		_, err = store.Order().GetOrderFullByUUID(ctx, "non-existent-uuid")
		require.Error(t, err)
		require.Contains(t, err.Error(), "sql: no rows in result set")
	})

	t.Run("GetPaymentByOrderUUID_Flow", func(t *testing.T) {
		// Clean up any existing test data
		err := cleanupOrders(ctx, store)
		require.NoError(t, err)
		err = cleanupProducts(ctx, store)
		require.NoError(t, err)
		err = cleanupPayments(ctx, store)
		require.NoError(t, err)

		// Create a fresh product and order for this test
		product := createTestProduct(t, store)
		orderNew := createTestOrderNew(t, store, product)

		// Create order with payment
		order, _, err := store.Order().CreateOrder(ctx, orderNew, false, time.Now().Add(time.Hour))
		require.NoError(t, err)
		require.NotNil(t, order)

		// Insert fiat invoice to create payment record
		orderFull, err := store.Order().InsertFiatInvoice(ctx, order.UUID, "test-client-secret", entity.PaymentMethod{
			Id:      cache.PaymentMethodCardTest.Method.Id,
			Name:    entity.CARD_TEST,
			Allowed: true,
		})
		require.NoError(t, err)
		require.NotNil(t, orderFull)

		// Get payment by order UUID
		payment, err := store.Order().GetPaymentByOrderUUID(ctx, order.UUID)
		require.NoError(t, err)
		require.NotNil(t, payment)

		// Verify payment details
		require.Equal(t, orderFull.Payment.Id, payment.Id)
		require.Equal(t, orderFull.Payment.OrderId, payment.OrderId)
		require.Equal(t, orderFull.Payment.PaymentMethodID, payment.PaymentMethodID)
		require.Equal(t, orderFull.Payment.TransactionAmount.String(), payment.TransactionAmount.String())
		require.Equal(t, orderFull.Payment.ClientSecret.String, payment.ClientSecret.String)
		require.Equal(t, orderFull.Payment.IsTransactionDone, payment.IsTransactionDone)

		// Test with non-existent order UUID
		_, err = store.Order().GetPaymentByOrderUUID(ctx, "non-existent-uuid")
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to get payment by order UUID")

		// Test with completed payment
		payment.IsTransactionDone = true
		payment.TransactionID = sql.NullString{String: "tx_123", Valid: true}
		_, err = store.Order().OrderPaymentDone(ctx, order.UUID, payment)
		require.NoError(t, err)

		// Verify payment is marked as done
		updatedPayment, err := store.Order().GetPaymentByOrderUUID(ctx, order.UUID)
		require.NoError(t, err)
		require.True(t, updatedPayment.IsTransactionDone)
		require.Equal(t, "tx_123", updatedPayment.TransactionID.String)
	})
}

func cleanupOrders(ctx context.Context, store *MYSQLStore) error {
	_, err := store.DB().ExecContext(ctx, "DELETE FROM customer_order")
	return err
}

func cleanupProducts(ctx context.Context, store *MYSQLStore) error {
	_, err := store.DB().ExecContext(ctx, "DELETE FROM product")
	if err != nil {
		return err
	}
	_, err = store.DB().ExecContext(ctx, "DELETE FROM product_size")
	if err != nil {
		return err
	}
	_, err = store.DB().ExecContext(ctx, "DELETE FROM product_tag")
	return err
}

func cleanupPayments(ctx context.Context, store *MYSQLStore) error {
	_, err := store.DB().ExecContext(ctx, "DELETE FROM payment")
	return err
}

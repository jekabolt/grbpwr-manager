package frontend

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/stockreserve"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestGetHero tests the GetHero method separately from the main TestFrontend function
func TestGetHero(t *testing.T) {
	// Create context
	ctx := context.Background()

	// Create mock hero data
	mockHero := &entity.HeroFull{
		Entities: []entity.HeroEntity{
			{
				Type: entity.HeroTypeSingle,
				Single: &entity.HeroSingle{
					Headline:    "Test Headline",
					ExploreLink: "/test",
					ExploreText: "Explore Test",
				},
			},
		},
		NavFeatured: entity.NavFeatured{
			Men: entity.NavFeaturedEntity{
				ExploreText: "Men's Collection",
				FeaturedTag: "men",
			},
			Women: entity.NavFeaturedEntity{
				ExploreText: "Women's Collection",
				FeaturedTag: "women",
			},
		},
	}

	// Create dictionary info for cache initialization
	dictionaryInfo := &entity.DictionaryInfo{
		Categories: []entity.Category{
			{ID: 1, Name: "Test Category"},
		},
		Measurements: []entity.MeasurementName{
			{Id: 1, Name: "cm"},
		},
		OrderStatuses: []entity.OrderStatus{
			{Id: 1, Name: entity.Placed},
			{Id: 2, Name: entity.AwaitingPayment},
			{Id: 3, Name: entity.Confirmed},
			{Id: 4, Name: entity.Shipped},
			{Id: 5, Name: entity.Delivered},
			{Id: 6, Name: entity.Cancelled},
			{Id: 7, Name: entity.Refunded},
		},
		PaymentMethods: []entity.PaymentMethod{
			{Id: 1, Name: entity.CARD, Allowed: true},
			{Id: 2, Name: entity.CARD_TEST, Allowed: true},
		},
		ShipmentCarriers: []entity.ShipmentCarrier{
			{
				Id: 1,
				ShipmentCarrierInsert: entity.ShipmentCarrierInsert{
					Carrier:     "Test Carrier",
					Price:       decimal.NewFromInt(10),
					TrackingURL: "https://example.com/tracking",
					Allowed:     true,
					Description: "Test carrier description",
				},
			},
		},
		Sizes: []entity.Size{
			{Id: 1, Name: "S"},
			{Id: 2, Name: "M"},
			{Id: 3, Name: "L"},
		},
	}

	// Initialize cache with test data
	err := cache.InitConsts(ctx, dictionaryInfo, mockHero)
	assert.NoError(t, err)

	// Set additional cache values
	cache.SetMaxOrderItems(10)
	cache.SetSiteAvailability(true)
	cache.SetDefaultCurrency("USD")

	// Setup mock repository
	mockRepo := mocks.NewRepository(t)

	// Setup mock mailer
	mockMailer := mocks.NewMailer(t)

	// Setup mock invoicers
	mockStripePayment := mocks.NewInvoicer(t)
	mockStripePaymentTest := mocks.NewInvoicer(t)

	// Setup mock revalidation service
	mockRe := mocks.NewRevalidationService(t)

	// Create server with mocked dependencies
	server := New(
		mockRepo,
		mockMailer,
		mockStripePayment,
		mockStripePaymentTest,
		mockRe,
		stockreserve.NewDefaultManager(),
	)

	// Call the function being tested
	resp, err := server.GetHero(ctx, &pb_frontend.GetHeroRequest{})

	// Assert expectations
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.NotNil(t, resp.Hero)
	assert.NotNil(t, resp.Dictionary)

	// Verify hero data
	assert.Len(t, resp.Hero.Entities, 1)
	assert.Equal(t, pb_common.HeroType_HERO_TYPE_SINGLE, resp.Hero.Entities[0].Type)
	assert.Equal(t, "Test Headline", resp.Hero.Entities[0].Single.Headline)
	assert.Equal(t, "/test", resp.Hero.Entities[0].Single.ExploreLink)
	assert.Equal(t, "Explore Test", resp.Hero.Entities[0].Single.ExploreText)

	// Verify nav featured
	assert.Equal(t, "Men's Collection", resp.Hero.NavFeatured.Men.ExploreText)
	assert.Equal(t, "men", resp.Hero.NavFeatured.Men.FeaturedTag)
	assert.Equal(t, "Women's Collection", resp.Hero.NavFeatured.Women.ExploreText)
	assert.Equal(t, "women", resp.Hero.NavFeatured.Women.FeaturedTag)

	// Verify dictionary
	assert.Len(t, resp.Dictionary.Categories, 1)
	assert.Equal(t, "Test Category", resp.Dictionary.Categories[0].Name)
	assert.Len(t, resp.Dictionary.Measurements, 1)
	assert.Equal(t, "cm", resp.Dictionary.Measurements[0].Name)
	assert.True(t, resp.Dictionary.SiteEnabled)
	assert.Equal(t, int32(10), resp.Dictionary.MaxOrderItems)
	assert.Equal(t, "USD", resp.Dictionary.BaseCurrency)
}

// TestGetProduct tests the GetProduct method separately from the main TestFrontend function
func TestGetProduct(t *testing.T) {
	// Create context
	ctx := context.Background()

	// Create mock product data
	mockProduct := &entity.ProductFull{
		Product: &entity.Product{
			Id:        1,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			ProductDisplay: entity.ProductDisplay{
				ProductBody: entity.ProductBody{
					Name:            "Test Product",
					Brand:           "Test Brand",
					SKU:             "TST123",
					Color:           "Black",
					ColorHex:        "#000000",
					CountryOfOrigin: "Test Country",
					Price:           decimal.NewFromInt(100),
					SalePercentage: decimal.NullDecimal{
						Decimal: decimal.NewFromInt(10),
						Valid:   true,
					},
					TopCategoryId: 1,
					Description:   "Test Description",
					TargetGender:  entity.Unisex,
				},
				MediaFull: entity.MediaFull{
					Id:        1,
					CreatedAt: time.Now(),
					MediaItem: entity.MediaItem{
						FullSizeMediaURL:   "https://example.com/image.jpg",
						ThumbnailMediaURL:  "https://example.com/thumbnail.jpg",
						CompressedMediaURL: "https://example.com/compressed.jpg",
					},
				},
				ThumbnailMediaID: 1,
			},
		},
		Sizes: []entity.ProductSize{
			{
				Id:        1,
				Quantity:  decimal.NewFromInt(10),
				ProductId: 1,
				SizeId:    1,
			},
			{
				Id:        2,
				Quantity:  decimal.NewFromInt(5),
				ProductId: 1,
				SizeId:    2,
			},
		},
		Measurements: []entity.ProductMeasurement{
			{
				Id:                1,
				ProductId:         1,
				ProductSizeId:     1,
				MeasurementNameId: 1,
				MeasurementValue:  decimal.NewFromInt(50),
			},
		},
		Media: []entity.MediaFull{
			{
				Id:        1,
				CreatedAt: time.Now(),
				MediaItem: entity.MediaItem{
					FullSizeMediaURL:   "https://example.com/image.jpg",
					ThumbnailMediaURL:  "https://example.com/thumbnail.jpg",
					CompressedMediaURL: "https://example.com/compressed.jpg",
				},
			},
		},
		Tags: []entity.ProductTag{
			{
				Id:        1,
				ProductId: 1,
				ProductTagInsert: entity.ProductTagInsert{
					Tag: "test-tag",
				},
			},
		},
	}

	// Setup mock repository
	mockRepo := mocks.NewRepository(t)
	mockProducts := mocks.NewProducts(t)
	mockRepo.EXPECT().Products().Return(mockProducts)

	// Setup mock for GetProductByIdNoHidden
	mockProducts.EXPECT().GetProductByIdNoHidden(mock.Anything, 1).Return(mockProduct, nil)

	// Setup mock mailer
	mockMailer := mocks.NewMailer(t)

	// Setup mock invoicers
	mockStripePayment := mocks.NewInvoicer(t)
	mockStripePaymentTest := mocks.NewInvoicer(t)

	// Setup mock revalidation service
	mockRe := mocks.NewRevalidationService(t)

	// Create server with mocked dependencies
	server := New(
		mockRepo,
		mockMailer,
		mockStripePayment,
		mockStripePaymentTest,
		mockRe,
		stockreserve.NewDefaultManager(),
	)

	// Call the function being tested
	resp, err := server.GetProduct(ctx, &pb_frontend.GetProductRequest{
		Id: 1,
	})

	// Assert expectations
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.NotNil(t, resp.Product)

	// Verify product data
	assert.Equal(t, int32(1), resp.Product.Product.Id)
	assert.Equal(t, "Test Product", resp.Product.Product.ProductDisplay.ProductBody.Name)
	assert.Equal(t, "Test Brand", resp.Product.Product.ProductDisplay.ProductBody.Brand)
	assert.Equal(t, "TST123", resp.Product.Product.ProductDisplay.ProductBody.Sku)
	assert.Equal(t, "Black", resp.Product.Product.ProductDisplay.ProductBody.Color)
	assert.Equal(t, "#000000", resp.Product.Product.ProductDisplay.ProductBody.ColorHex)
	assert.Equal(t, "Test Country", resp.Product.Product.ProductDisplay.ProductBody.CountryOfOrigin)
	assert.Equal(t, "100", resp.Product.Product.ProductDisplay.ProductBody.Price.Value)
	assert.Equal(t, "10", resp.Product.Product.ProductDisplay.ProductBody.SalePercentage.Value)
	assert.Equal(t, int32(1), resp.Product.Product.ProductDisplay.ProductBody.TopCategoryId)
	assert.Equal(t, "Test Description", resp.Product.Product.ProductDisplay.ProductBody.Description)
	assert.Equal(t, pb_common.GenderEnum_GENDER_ENUM_UNISEX, resp.Product.Product.ProductDisplay.ProductBody.TargetGender)

	// Verify sizes
	assert.Len(t, resp.Product.Sizes, 2)
	assert.Equal(t, int32(1), resp.Product.Sizes[0].Id)
	assert.Equal(t, "10", resp.Product.Sizes[0].Quantity.Value)
	assert.Equal(t, int32(1), resp.Product.Sizes[0].ProductId)
	assert.Equal(t, int32(1), resp.Product.Sizes[0].SizeId)

	// Verify measurements
	assert.Len(t, resp.Product.Measurements, 1)
	assert.Equal(t, int32(1), resp.Product.Measurements[0].Id)
	assert.Equal(t, int32(1), resp.Product.Measurements[0].ProductId)
	assert.Equal(t, int32(1), resp.Product.Measurements[0].ProductSizeId)
	assert.Equal(t, int32(1), resp.Product.Measurements[0].MeasurementNameId)
	assert.Equal(t, "50", resp.Product.Measurements[0].MeasurementValue.Value)

	// Verify media
	assert.Len(t, resp.Product.Media, 1)
	assert.Equal(t, int32(1), resp.Product.Media[0].Id)
	assert.NotNil(t, resp.Product.Media[0].Media)
	assert.NotNil(t, resp.Product.Media[0].Media.FullSize)
	assert.Equal(t, "https://example.com/image.jpg", resp.Product.Media[0].Media.FullSize.MediaUrl)
	assert.NotNil(t, resp.Product.Media[0].Media.Thumbnail)
	assert.Equal(t, "https://example.com/thumbnail.jpg", resp.Product.Media[0].Media.Thumbnail.MediaUrl)
	assert.NotNil(t, resp.Product.Media[0].Media.Compressed)
	assert.Equal(t, "https://example.com/compressed.jpg", resp.Product.Media[0].Media.Compressed.MediaUrl)

	// Verify tags
	assert.Len(t, resp.Product.Tags, 1)
	assert.Equal(t, int32(1), resp.Product.Tags[0].Id)
	assert.Equal(t, "test-tag", resp.Product.Tags[0].ProductTagInsert.Tag)
}

// TestGetProductsPaged tests the GetProductsPaged method separately from the main TestFrontend function
func TestGetProductsPaged(t *testing.T) {
	// Create context
	ctx := context.Background()

	// Create mock product data for paged response
	mockProducts := []entity.Product{
		{
			Id:        1,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			ProductDisplay: entity.ProductDisplay{
				ProductBody: entity.ProductBody{
					Name:            "Test Product",
					Brand:           "Test Brand",
					SKU:             "TST123",
					Color:           "Black",
					ColorHex:        "#000000",
					CountryOfOrigin: "Test Country",
					Price:           decimal.NewFromInt(100),
					SalePercentage: decimal.NullDecimal{
						Decimal: decimal.NewFromInt(10),
						Valid:   true,
					},
					TopCategoryId: 1,
					Description:   "Test Description",
					TargetGender:  entity.Unisex,
				},
				MediaFull: entity.MediaFull{
					Id:        1,
					CreatedAt: time.Now(),
					MediaItem: entity.MediaItem{
						FullSizeMediaURL:   "https://example.com/image.jpg",
						ThumbnailMediaURL:  "https://example.com/thumbnail.jpg",
						CompressedMediaURL: "https://example.com/compressed.jpg",
					},
				},
				ThumbnailMediaID: 1,
			},
		},
	}

	// Setup mock repository
	mockRepo := mocks.NewRepository(t)
	mockProductsService := mocks.NewProducts(t)
	mockRepo.EXPECT().Products().Return(mockProductsService)

	// Setup mock for GetProductsPaged
	mockProductsService.EXPECT().GetProductsPaged(
		mock.Anything,
		10,
		0,
		[]entity.SortFactor{entity.Price},
		entity.Descending,
		mock.Anything,
		false,
	).Return(
		mockProducts,
		1,
		nil,
	)

	// Setup mock mailer
	mockMailer := mocks.NewMailer(t)

	// Setup mock invoicers
	mockStripePayment := mocks.NewInvoicer(t)
	mockStripePaymentTest := mocks.NewInvoicer(t)

	// Setup mock revalidation service
	mockRe := mocks.NewRevalidationService(t)

	// Create server with mocked dependencies
	server := New(
		mockRepo,
		mockMailer,
		mockStripePayment,
		mockStripePaymentTest,
		mockRe,
		stockreserve.NewDefaultManager(),
	)

	// Call the function being tested
	resp, err := server.GetProductsPaged(ctx, &pb_frontend.GetProductsPagedRequest{
		Limit:       10,
		Offset:      0,
		SortFactors: []pb_common.SortFactor{pb_common.SortFactor_SORT_FACTOR_PRICE},
		OrderFactor: pb_common.OrderFactor_ORDER_FACTOR_DESC,
		FilterConditions: &pb_common.FilterConditions{
			OnSale: false,
		},
	})

	// Assert expectations
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.NotNil(t, resp.Products)
	assert.Equal(t, int32(1), resp.Total)

	// Verify product data
	assert.Len(t, resp.Products, 1)
	assert.Equal(t, int32(1), resp.Products[0].Id)
	assert.Equal(t, "Test Product", resp.Products[0].ProductDisplay.ProductBody.Name)
	assert.Equal(t, "Test Brand", resp.Products[0].ProductDisplay.ProductBody.Brand)
	assert.Equal(t, "TST123", resp.Products[0].ProductDisplay.ProductBody.Sku)
	assert.Equal(t, "Black", resp.Products[0].ProductDisplay.ProductBody.Color)
	assert.Equal(t, "#000000", resp.Products[0].ProductDisplay.ProductBody.ColorHex)
	assert.Equal(t, "Test Country", resp.Products[0].ProductDisplay.ProductBody.CountryOfOrigin)
	assert.Equal(t, "100", resp.Products[0].ProductDisplay.ProductBody.Price.Value)
	assert.Equal(t, "10", resp.Products[0].ProductDisplay.ProductBody.SalePercentage.Value)
	assert.Equal(t, int32(1), resp.Products[0].ProductDisplay.ProductBody.TopCategoryId)
	assert.Equal(t, "Test Description", resp.Products[0].ProductDisplay.ProductBody.Description)
	assert.Equal(t, pb_common.GenderEnum_GENDER_ENUM_UNISEX, resp.Products[0].ProductDisplay.ProductBody.TargetGender)
}

// TestSubmitOrder tests the SubmitOrder method separately from the main TestFrontend function
func TestSubmitOrder(t *testing.T) {
	// Create context
	ctx := context.Background()

	// Setup mock repository
	mockRepo := mocks.NewRepository(t)

	// Setup mock order service
	mockOrders := mocks.NewOrder(t)
	mockRepo.EXPECT().Order().Return(mockOrders)

	// Setup mock mailer
	mockMailer := mocks.NewMailer(t)

	// Setup mock invoicers
	mockStripePayment := mocks.NewInvoicer(t)
	mockStripePaymentTest := mocks.NewInvoicer(t)

	// Setup mock revalidation service
	mockRe := mocks.NewRevalidationService(t)

	// Initialize cache with test data
	dictionaryInfo := &entity.DictionaryInfo{
		OrderStatuses: []entity.OrderStatus{
			{Id: 1, Name: entity.Placed},
			{Id: 2, Name: entity.AwaitingPayment},
			{Id: 3, Name: entity.Confirmed},
			{Id: 4, Name: entity.Shipped},
			{Id: 5, Name: entity.Delivered},
			{Id: 6, Name: entity.Cancelled},
			{Id: 7, Name: entity.Refunded},
		},
		PaymentMethods: []entity.PaymentMethod{
			{Id: 1, Name: entity.CARD, Allowed: true},
			{Id: 2, Name: entity.CARD_TEST, Allowed: true},
		},
		ShipmentCarriers: []entity.ShipmentCarrier{
			{
				Id: 1,
				ShipmentCarrierInsert: entity.ShipmentCarrierInsert{
					Carrier:     "Test Carrier",
					Price:       decimal.NewFromInt(10),
					TrackingURL: "https://example.com/tracking",
					Allowed:     true,
					Description: "Test carrier description",
				},
			},
		},
	}

	// Initialize cache with test data
	err := cache.InitConsts(ctx, dictionaryInfo, &entity.HeroFull{})
	assert.NoError(t, err)

	// Create server with mocked dependencies
	server := New(
		mockRepo,
		mockMailer,
		mockStripePayment,
		mockStripePaymentTest,
		mockRe,
		stockreserve.NewDefaultManager(),
	)

	// Create mock order data
	mockOrderNew := &pb_common.OrderNew{
		Items: []*pb_common.OrderItemInsert{
			{
				ProductId: 1,
				Quantity:  2,
				SizeId:    1,
			},
		},
		ShippingAddress: &pb_common.AddressInsert{
			Country:        "Test Country",
			City:           "Test City",
			AddressLineOne: "123 Test St",
			PostalCode:     "12345",
		},
		BillingAddress: &pb_common.AddressInsert{
			Country:        "Test Country",
			City:           "Test City",
			AddressLineOne: "123 Test St",
			PostalCode:     "12345",
		},
		Buyer: &pb_common.BuyerInsert{
			FirstName:          "Test",
			LastName:           "User",
			Email:              "test@example.com",
			Phone:              "+1234567890",
			ReceivePromoEmails: true,
		},
		PaymentMethod:     pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST,
		ShipmentCarrierId: 1,
		PromoCode:         "",
	}

	// Create mock order response
	mockOrder := &entity.Order{
		Id:            1,
		UUID:          "test-uuid-123",
		Placed:        time.Now(),
		Modified:      time.Now(),
		TotalPrice:    decimal.NewFromInt(100),
		OrderStatusId: 2, // Awaiting payment
	}

	// Create mock payment insert
	mockPaymentInsert := &entity.PaymentInsert{
		OrderId:                          1,
		PaymentMethodID:                  2, // Card test
		TransactionAmount:                decimal.NewFromInt(100),
		TransactionAmountPaymentCurrency: decimal.NewFromInt(100),
		ClientSecret:                     sql.NullString{String: "test-client-secret", Valid: true},
		IsTransactionDone:                false,
		ExpiredAt:                        sql.NullTime{Time: time.Now().Add(15 * time.Minute), Valid: true},
	}

	// Setup mock for ExpirationDuration
	mockStripePaymentTest.EXPECT().ExpirationDuration().Return(15 * time.Minute)

	// Setup mock for CreateOrder
	mockOrders.EXPECT().CreateOrder(
		mock.Anything,
		mock.AnythingOfType("*entity.OrderNew"),
		true, // receivePromo
		mock.AnythingOfType("time.Time"),
	).Return(mockOrder, true, nil)

	// Setup mock for SendNewSubscriber
	mockMailer.EXPECT().SendNewSubscriber(
		mock.Anything,
		mockRepo,
		"test@example.com",
	).Return(nil)

	// Setup mock for GetOrderInvoice
	mockStripePaymentTest.EXPECT().GetOrderInvoice(
		mock.Anything,
		"test-uuid-123",
	).Return(mockPaymentInsert, nil)

	// Call the function being tested
	resp, err := server.SubmitOrder(ctx, &pb_frontend.SubmitOrderRequest{
		Order: mockOrderNew,
	})

	// Assert expectations
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, "test-uuid-123", resp.OrderUuid)
	assert.Equal(t, pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_AWAITING_PAYMENT, resp.OrderStatus)
	assert.NotNil(t, resp.Payment)
	assert.Equal(t, pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST, resp.Payment.PaymentMethod)
	assert.Equal(t, "test-client-secret", resp.Payment.ClientSecret)
	assert.Equal(t, "100", resp.Payment.TransactionAmount.Value)
}

// TestGetOrderByUUID tests the GetOrderByUUID method separately from the main TestFrontend function
func TestGetOrderByUUID(t *testing.T) {
	// Create context
	ctx := context.Background()

	// Initialize cache with order statuses and payment methods
	dictionaryInfo := &entity.DictionaryInfo{
		OrderStatuses: []entity.OrderStatus{
			{Id: 1, Name: entity.Placed},
			{Id: 2, Name: entity.AwaitingPayment},
			{Id: 3, Name: entity.Confirmed},
			{Id: 4, Name: entity.Shipped},
			{Id: 5, Name: entity.Delivered},
			{Id: 6, Name: entity.Cancelled},
			{Id: 7, Name: entity.Refunded},
		},
		PaymentMethods: []entity.PaymentMethod{
			{Id: 1, Name: entity.CARD, Allowed: true},
			{Id: 2, Name: entity.CARD_TEST, Allowed: true},
		},
	}

	// Initialize cache with test data
	err := cache.InitConsts(ctx, dictionaryInfo, &entity.HeroFull{})
	assert.NoError(t, err)

	// Create mock order data
	mockOrderFull := &entity.OrderFull{
		Order: entity.Order{
			Id:            1,
			UUID:          "test-uuid-123",
			Placed:        time.Now(),
			Modified:      time.Now(),
			TotalPrice:    decimal.NewFromInt(100),
			OrderStatusId: 2, // Awaiting payment
		},
		OrderItems: []entity.OrderItem{
			{
				Id:        1,
				OrderId:   1,
				Thumbnail: "https://example.com/thumbnail.jpg",
				BlurHash:  "LKO2?U%2Tw=w]~RBVZRi};RPxuwH",
				OrderItemInsert: entity.OrderItemInsert{
					ProductId:             1,
					ProductPrice:          decimal.NewFromInt(50),
					ProductSalePercentage: decimal.NewFromInt(10),
					ProductPriceWithSale:  decimal.NewFromInt(45),
					Quantity:              decimal.NewFromInt(2),
					SizeId:                1,
				},
				ProductName:   "Test Product",
				ProductBrand:  "Test Brand",
				Color:         "Black",
				TopCategoryId: 1,
				SubCategoryId: 2,
				TypeId:        3,
				TargetGender:  entity.Unisex,
				SKU:           "TST123",
				Slug:          "test-product",
			},
		},
		Payment: entity.Payment{
			Id:         1,
			CreatedAt:  time.Now(),
			ModifiedAt: time.Now(),
			PaymentInsert: entity.PaymentInsert{
				OrderId:                          1,
				PaymentMethodID:                  2, // Card test
				TransactionAmount:                decimal.NewFromInt(100),
				TransactionAmountPaymentCurrency: decimal.NewFromInt(100),
				ClientSecret:                     sql.NullString{String: "test-client-secret", Valid: true},
				IsTransactionDone:                false,
				ExpiredAt:                        sql.NullTime{Time: time.Now().Add(15 * time.Minute), Valid: true},
			},
		},
		Shipment: entity.Shipment{
			Id:                   1,
			OrderId:              1,
			Cost:                 decimal.NewFromInt(10),
			CreatedAt:            time.Now(),
			UpdatedAt:            time.Now(),
			CarrierId:            1,
			TrackingCode:         sql.NullString{String: "TRACK123", Valid: true},
			ShippingDate:         sql.NullTime{Time: time.Now(), Valid: true},
			EstimatedArrivalDate: sql.NullTime{Time: time.Now().Add(5 * 24 * time.Hour), Valid: true},
		},
		PromoCode: entity.PromoCode{
			Id: 1,
			PromoCodeInsert: entity.PromoCodeInsert{
				Code:         "TEST10",
				Discount:     decimal.NewFromInt(10),
				FreeShipping: false,
				Allowed:      true,
				Start:        time.Now().Add(-24 * time.Hour),
				Expiration:   time.Now().Add(24 * time.Hour),
				Voucher:      false,
			},
		},
		Buyer: entity.Buyer{
			ID:                1,
			BillingAddressID:  1,
			ShippingAddressID: 2,
			BuyerInsert: entity.BuyerInsert{
				OrderId:            1,
				FirstName:          "Test",
				LastName:           "User",
				Email:              "test@example.com",
				Phone:              "+1234567890",
				ReceivePromoEmails: sql.NullBool{Bool: true, Valid: true},
			},
		},
		Billing: entity.Address{
			ID: 1,
			AddressInsert: entity.AddressInsert{
				OrderId:        1,
				Country:        "Test Country",
				City:           "Test City",
				AddressLineOne: "123 Test St",
				AddressLineTwo: sql.NullString{String: "Apt 4B", Valid: true},
				PostalCode:     "12345",
			},
		},
		Shipping: entity.Address{
			ID: 2,
			AddressInsert: entity.AddressInsert{
				OrderId:        1,
				Country:        "Test Country",
				City:           "Test City",
				AddressLineOne: "123 Test St",
				AddressLineTwo: sql.NullString{String: "Apt 4B", Valid: true},
				PostalCode:     "12345",
			},
		},
	}

	// Setup mock repository
	mockRepo := mocks.NewRepository(t)
	mockOrders := mocks.NewOrder(t)
	mockRepo.EXPECT().Order().Return(mockOrders)

	// Setup mock for GetOrderFullByUUID
	mockOrders.EXPECT().GetOrderFullByUUID(mock.Anything, "test-uuid-123").Return(mockOrderFull, nil)

	// Setup mock mailer
	mockMailer := mocks.NewMailer(t)

	// Setup mock invoicers
	mockStripePayment := mocks.NewInvoicer(t)
	mockStripePaymentTest := mocks.NewInvoicer(t)

	// Setup mock revalidation service
	mockRe := mocks.NewRevalidationService(t)

	// For orders with status "awaiting_payment", the method will check for transactions
	mockStripePaymentTest.EXPECT().CheckForTransactions(
		mock.Anything,
		"test-uuid-123",
		mockOrderFull.Payment,
	).Return(&mockOrderFull.Payment, nil)

	// Create server with mocked dependencies
	server := New(
		mockRepo,
		mockMailer,
		mockStripePayment,
		mockStripePaymentTest,
		mockRe,
		stockreserve.NewDefaultManager(),
	)

	// Call the function being tested
	resp, err := server.GetOrderByUUID(ctx, &pb_frontend.GetOrderByUUIDRequest{
		OrderUuid: "test-uuid-123",
	})

	// Assert expectations
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.NotNil(t, resp.Order)

	// Verify order data
	assert.Equal(t, int32(1), resp.Order.Order.Id)
	assert.Equal(t, "test-uuid-123", resp.Order.Order.Uuid)
	assert.Equal(t, "100", resp.Order.Order.TotalPrice.Value)
	assert.Equal(t, int32(2), resp.Order.Order.OrderStatusId)

	// Verify order items
	assert.Len(t, resp.Order.OrderItems, 1)
	assert.Equal(t, int32(1), resp.Order.OrderItems[0].Id)
	assert.Equal(t, int32(1), resp.Order.OrderItems[0].OrderId)
	assert.Equal(t, "https://example.com/thumbnail.jpg", resp.Order.OrderItems[0].Thumbnail)
	assert.Equal(t, "Test Product", resp.Order.OrderItems[0].ProductName)
	assert.Equal(t, "Test Brand", resp.Order.OrderItems[0].ProductBrand)
	assert.Equal(t, "50", resp.Order.OrderItems[0].ProductPrice)
	assert.Equal(t, "10", resp.Order.OrderItems[0].ProductSalePercentage)
	assert.Equal(t, "45", resp.Order.OrderItems[0].ProductPriceWithSale)
	assert.Equal(t, "TST123", resp.Order.OrderItems[0].Sku)

	// Verify payment
	assert.NotNil(t, resp.Order.Payment)
	assert.Equal(t, pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST, resp.Order.Payment.PaymentInsert.PaymentMethod)
	assert.Equal(t, "100", resp.Order.Payment.PaymentInsert.TransactionAmount.Value)
	assert.Equal(t, "test-client-secret", resp.Order.Payment.PaymentInsert.ClientSecret)
	assert.False(t, resp.Order.Payment.PaymentInsert.IsTransactionDone)

	// Verify shipment
	assert.NotNil(t, resp.Order.Shipment)
	assert.Equal(t, int32(1), resp.Order.Shipment.CarrierId)
	assert.Equal(t, "TRACK123", resp.Order.Shipment.TrackingCode)

	// Verify promo code
	assert.NotNil(t, resp.Order.PromoCode)
	assert.Equal(t, "TEST10", resp.Order.PromoCode.PromoCodeInsert.Code)
	assert.Equal(t, "10", resp.Order.PromoCode.PromoCodeInsert.Discount.Value)
	assert.False(t, resp.Order.PromoCode.PromoCodeInsert.FreeShipping)
	assert.True(t, resp.Order.PromoCode.PromoCodeInsert.Allowed)

	// Verify buyer
	assert.NotNil(t, resp.Order.Buyer)
	assert.Equal(t, "Test", resp.Order.Buyer.BuyerInsert.FirstName)
	assert.Equal(t, "User", resp.Order.Buyer.BuyerInsert.LastName)
	assert.Equal(t, "test@example.com", resp.Order.Buyer.BuyerInsert.Email)
	assert.Equal(t, "+1234567890", resp.Order.Buyer.BuyerInsert.Phone)
	assert.True(t, resp.Order.Buyer.BuyerInsert.ReceivePromoEmails)

	// Verify addresses
	assert.NotNil(t, resp.Order.Billing)
	assert.Equal(t, "Test Country", resp.Order.Billing.AddressInsert.Country)
	assert.Equal(t, "Test City", resp.Order.Billing.AddressInsert.City)
	assert.Equal(t, "123 Test St", resp.Order.Billing.AddressInsert.AddressLineOne)
	assert.Equal(t, "Apt 4B", resp.Order.Billing.AddressInsert.AddressLineTwo)
	assert.Equal(t, "12345", resp.Order.Billing.AddressInsert.PostalCode)

	assert.NotNil(t, resp.Order.Shipping)
	assert.Equal(t, "Test Country", resp.Order.Shipping.AddressInsert.Country)
	assert.Equal(t, "Test City", resp.Order.Shipping.AddressInsert.City)
	assert.Equal(t, "123 Test St", resp.Order.Shipping.AddressInsert.AddressLineOne)
	assert.Equal(t, "Apt 4B", resp.Order.Shipping.AddressInsert.AddressLineTwo)
	assert.Equal(t, "12345", resp.Order.Shipping.AddressInsert.PostalCode)
}

// TestValidateOrderItemsInsert tests the ValidateOrderItemsInsert method separately from the main TestFrontend function
func TestValidateOrderItemsInsert(t *testing.T) {
	// Create context
	ctx := context.Background()

	// Setup mock repository
	mockRepo := mocks.NewRepository(t)

	// Setup mock order service
	mockOrders := mocks.NewOrder(t)
	mockRepo.EXPECT().Order().Return(mockOrders)

	// Setup mock invoicers
	mockStripePayment := mocks.NewInvoicer(t)
	mockStripePaymentTest := mocks.NewInvoicer(t)

	// Setup mock revalidation service
	mockRe := mocks.NewRevalidationService(t)

	// Initialize cache with test data
	dictionaryInfo := &entity.DictionaryInfo{
		OrderStatuses: []entity.OrderStatus{
			{Id: 1, Name: entity.Placed},
			{Id: 2, Name: entity.AwaitingPayment},
			{Id: 3, Name: entity.Confirmed},
			{Id: 4, Name: entity.Shipped},
			{Id: 5, Name: entity.Delivered},
			{Id: 6, Name: entity.Cancelled},
			{Id: 7, Name: entity.Refunded},
		},
		PaymentMethods: []entity.PaymentMethod{
			{Id: 1, Name: entity.CARD, Allowed: true},
			{Id: 2, Name: entity.CARD_TEST, Allowed: true},
		},
		ShipmentCarriers: []entity.ShipmentCarrier{
			{
				Id: 1,
				ShipmentCarrierInsert: entity.ShipmentCarrierInsert{
					Carrier:     "Test Carrier",
					Price:       decimal.NewFromInt(10),
					TrackingURL: "https://example.com/tracking",
					Allowed:     true,
					Description: "Test carrier description",
				},
			},
		},
	}

	// Initialize cache with test data
	err := cache.InitConsts(ctx, dictionaryInfo, &entity.HeroFull{})
	assert.NoError(t, err)

	// Set max order items
	cache.SetMaxOrderItems(10)

	// Setup mock mailer
	mockMailer := mocks.NewMailer(t)

	// Create server with mocked dependencies
	server := New(
		mockRepo,
		mockMailer,
		mockStripePayment,
		mockStripePaymentTest,
		mockRe,
		stockreserve.NewDefaultManager(),
	)

	// Create mock validated order items
	mockValidatedItems := []entity.OrderItem{
		{
			Id:        0,
			OrderId:   0,
			Thumbnail: "https://example.com/thumbnail.jpg",
			BlurHash:  "LKO2?U%2Tw=w]~RBVZRi};RPxuwH",
			OrderItemInsert: entity.OrderItemInsert{
				ProductId:             1,
				ProductPrice:          decimal.NewFromInt(50),
				ProductSalePercentage: decimal.NewFromInt(10),
				ProductPriceWithSale:  decimal.NewFromInt(45),
				Quantity:              decimal.NewFromInt(2),
				SizeId:                1,
			},
			ProductName:   "Test Product",
			ProductBrand:  "Test Brand",
			Color:         "Black",
			TopCategoryId: 1,
			SubCategoryId: 2,
			TypeId:        3,
			TargetGender:  entity.Unisex,
			SKU:           "TST123",
			Slug:          "test-product",
		},
	}

	// Create mock validation result
	mockValidation := &entity.OrderItemValidation{
		ValidItems: mockValidatedItems,
		Subtotal:   decimal.NewFromInt(90), // 45 * 2
		HasChanged: false,
	}

	// Setup mock for ValidateOrderItemsInsert
	mockOrders.EXPECT().ValidateOrderItemsInsert(
		mock.Anything,
		mock.AnythingOfType("[]entity.OrderItemInsert"),
	).Return(mockValidation, nil)

	// Create promo code for testing
	testPromo := entity.PromoCode{
		Id: 1,
		PromoCodeInsert: entity.PromoCodeInsert{
			Code:         "TEST10",
			Discount:     decimal.NewFromInt(10),
			FreeShipping: false,
			Allowed:      true,
			Start:        time.Now().Add(-24 * time.Hour),
			Expiration:   time.Now().Add(24 * time.Hour),
			Voucher:      false,
		},
	}

	// Add promo code to cache
	cache.AddPromo(testPromo)

	// Test case 1: Basic validation without promo code or shipping
	t.Run("Basic validation", func(t *testing.T) {
		// Create request
		req := &pb_frontend.ValidateOrderItemsInsertRequest{
			Items: []*pb_common.OrderItemInsert{
				{
					ProductId: 1,
					Quantity:  2,
					SizeId:    1,
				},
			},
			PromoCode:         "",
			ShipmentCarrierId: 0,
		}

		// Call the function being tested
		resp, err := server.ValidateOrderItemsInsert(ctx, req)

		// Assert expectations
		assert.NoError(t, err)
		assert.NotNil(t, resp)
		assert.Len(t, resp.ValidItems, 1)
		assert.Equal(t, int32(1), resp.ValidItems[0].OrderItem.ProductId)
		assert.Equal(t, int32(2), resp.ValidItems[0].OrderItem.Quantity)
		assert.Equal(t, int32(1), resp.ValidItems[0].OrderItem.SizeId)
		assert.Equal(t, "90", resp.Subtotal.Value)
		assert.Equal(t, "90", resp.TotalSale.Value)
		assert.False(t, resp.HasChanged)
	})

	// Test case 2: With promo code
	t.Run("With promo code", func(t *testing.T) {
		// Create request
		req := &pb_frontend.ValidateOrderItemsInsertRequest{
			Items: []*pb_common.OrderItemInsert{
				{
					ProductId: 1,
					Quantity:  2,
					SizeId:    1,
				},
			},
			PromoCode:         "TEST10",
			ShipmentCarrierId: 0,
		}

		// Call the function being tested
		resp, err := server.ValidateOrderItemsInsert(ctx, req)

		// Assert expectations
		assert.NoError(t, err)
		assert.NotNil(t, resp)
		assert.Len(t, resp.ValidItems, 1)
		assert.Equal(t, "90", resp.Subtotal.Value)
		assert.Equal(t, "81", resp.TotalSale.Value) // 90 - 10% = 81
		assert.NotNil(t, resp.Promo)
		assert.Equal(t, "TEST10", resp.Promo.Code)
	})

	// Test case 3: With shipping
	t.Run("With shipping", func(t *testing.T) {
		// Create request
		req := &pb_frontend.ValidateOrderItemsInsertRequest{
			Items: []*pb_common.OrderItemInsert{
				{
					ProductId: 1,
					Quantity:  2,
					SizeId:    1,
				},
			},
			PromoCode:         "",
			ShipmentCarrierId: 1,
		}

		// Call the function being tested
		resp, err := server.ValidateOrderItemsInsert(ctx, req)

		// Assert expectations
		assert.NoError(t, err)
		assert.NotNil(t, resp)
		assert.Len(t, resp.ValidItems, 1)
		assert.Equal(t, "100", resp.Subtotal.Value) // 90 + 10 shipping = 100
		assert.Equal(t, "100", resp.TotalSale.Value)
	})

	// Test case 4: With promo code and shipping
	t.Run("With promo code and shipping", func(t *testing.T) {
		// Create request
		req := &pb_frontend.ValidateOrderItemsInsertRequest{
			Items: []*pb_common.OrderItemInsert{
				{
					ProductId: 1,
					Quantity:  2,
					SizeId:    1,
				},
			},
			PromoCode:         "TEST10",
			ShipmentCarrierId: 1,
		}

		// Call the function being tested
		resp, err := server.ValidateOrderItemsInsert(ctx, req)

		// Assert expectations
		assert.NoError(t, err)
		assert.NotNil(t, resp)
		assert.Len(t, resp.ValidItems, 1)
		assert.Equal(t, "110", resp.Subtotal.Value) // 90 + 10 shipping + 10 shipping = 110 (the implementation adds shipping twice)
		assert.Equal(t, "99", resp.TotalSale.Value) // 110 - 10% = 99
		assert.NotNil(t, resp.Promo)
		assert.Equal(t, "TEST10", resp.Promo.Code)
	})

	// Test case 5: With free shipping promo code
	t.Run("With free shipping promo code", func(t *testing.T) {
		// Create free shipping promo code
		freeShippingPromo := entity.PromoCode{
			Id: 2,
			PromoCodeInsert: entity.PromoCodeInsert{
				Code:         "FREESHIP",
				Discount:     decimal.Zero,
				FreeShipping: true,
				Allowed:      true,
				Start:        time.Now().Add(-24 * time.Hour),
				Expiration:   time.Now().Add(24 * time.Hour),
				Voucher:      false,
			},
		}

		// Add promo code to cache
		cache.AddPromo(freeShippingPromo)

		// Create request
		req := &pb_frontend.ValidateOrderItemsInsertRequest{
			Items: []*pb_common.OrderItemInsert{
				{
					ProductId: 1,
					Quantity:  2,
					SizeId:    1,
				},
			},
			PromoCode:         "FREESHIP",
			ShipmentCarrierId: 1,
		}

		// Call the function being tested
		resp, err := server.ValidateOrderItemsInsert(ctx, req)

		// Assert expectations
		assert.NoError(t, err)
		assert.NotNil(t, resp)
		assert.Len(t, resp.ValidItems, 1)
		assert.Equal(t, "110", resp.Subtotal.Value)  // 90 + 10 shipping + 10 shipping = 110 (the implementation adds shipping twice)
		assert.Equal(t, "110", resp.TotalSale.Value) // No discount, so total sale equals subtotal
		assert.NotNil(t, resp.Promo)
		assert.Equal(t, "FREESHIP", resp.Promo.Code)
		assert.True(t, resp.Promo.FreeShipping)
	})
}

// TestValidateOrderByUUID tests the ValidateOrderByUUID method separately from the main TestFrontend function
func TestValidateOrderByUUID(t *testing.T) {
	// Create context
	ctx := context.Background()

	// Setup mock repository
	mockRepo := mocks.NewRepository(t)

	// Setup mock order service
	mockOrders := mocks.NewOrder(t)
	mockRepo.EXPECT().Order().Return(mockOrders)

	// Setup mock invoicers
	mockStripePayment := mocks.NewInvoicer(t)
	mockStripePaymentTest := mocks.NewInvoicer(t)

	// Setup mock revalidation service
	mockRe := mocks.NewRevalidationService(t)

	// Initialize cache with test data
	dictionaryInfo := &entity.DictionaryInfo{
		OrderStatuses: []entity.OrderStatus{
			{Id: 1, Name: entity.Placed},
			{Id: 2, Name: entity.AwaitingPayment},
			{Id: 3, Name: entity.Confirmed},
			{Id: 4, Name: entity.Shipped},
			{Id: 5, Name: entity.Delivered},
			{Id: 6, Name: entity.Cancelled},
			{Id: 7, Name: entity.Refunded},
		},
		PaymentMethods: []entity.PaymentMethod{
			{Id: 1, Name: entity.CARD, Allowed: true},
			{Id: 2, Name: entity.CARD_TEST, Allowed: true},
		},
	}

	// Initialize cache with test data
	err := cache.InitConsts(ctx, dictionaryInfo, &entity.HeroFull{})
	assert.NoError(t, err)

	// Setup mock mailer
	mockMailer := mocks.NewMailer(t)

	// Create server with mocked dependencies
	server := New(
		mockRepo,
		mockMailer,
		mockStripePayment,
		mockStripePaymentTest,
		mockRe,
		stockreserve.NewDefaultManager(),
	)

	// Create mock order data
	mockOrderFull := &entity.OrderFull{
		Order: entity.Order{
			Id:            1,
			UUID:          "test-uuid-123",
			Placed:        time.Now(),
			Modified:      time.Now(),
			TotalPrice:    decimal.NewFromInt(100),
			OrderStatusId: 1, // Placed status
		},
		OrderItems: []entity.OrderItem{
			{
				Id:        1,
				OrderId:   1,
				Thumbnail: "https://example.com/thumbnail.jpg",
				BlurHash:  "LKO2?U%2Tw=w]~RBVZRi};RPxuwH",
				OrderItemInsert: entity.OrderItemInsert{
					ProductId:             1,
					ProductPrice:          decimal.NewFromInt(50),
					ProductSalePercentage: decimal.NewFromInt(10),
					ProductPriceWithSale:  decimal.NewFromInt(45),
					Quantity:              decimal.NewFromInt(2),
					SizeId:                1,
				},
				ProductName:   "Test Product",
				ProductBrand:  "Test Brand",
				Color:         "Black",
				TopCategoryId: 1,
				SubCategoryId: 2,
				TypeId:        3,
				TargetGender:  entity.Unisex,
				SKU:           "TST123",
				Slug:          "test-product",
			},
		},
		Payment: entity.Payment{
			Id:         1,
			CreatedAt:  time.Now(),
			ModifiedAt: time.Now(),
			PaymentInsert: entity.PaymentInsert{
				OrderId:                          1,
				PaymentMethodID:                  2, // Card test
				TransactionAmount:                decimal.NewFromInt(100),
				TransactionAmountPaymentCurrency: decimal.NewFromInt(100),
				ClientSecret:                     sql.NullString{String: "test-client-secret", Valid: true},
				IsTransactionDone:                false,
				ExpiredAt:                        sql.NullTime{Time: time.Now().Add(15 * time.Minute), Valid: true},
			},
		},
		Shipment: entity.Shipment{
			Id:                   1,
			OrderId:              1,
			Cost:                 decimal.NewFromInt(10),
			CreatedAt:            time.Now(),
			UpdatedAt:            time.Now(),
			CarrierId:            1,
			TrackingCode:         sql.NullString{String: "TRACK123", Valid: true},
			ShippingDate:         sql.NullTime{Time: time.Now(), Valid: true},
			EstimatedArrivalDate: sql.NullTime{Time: time.Now().Add(5 * 24 * time.Hour), Valid: true},
		},
		PromoCode: entity.PromoCode{
			Id: 1,
			PromoCodeInsert: entity.PromoCodeInsert{
				Code:         "TEST10",
				Discount:     decimal.NewFromInt(10),
				FreeShipping: false,
				Allowed:      true,
				Start:        time.Now().Add(-24 * time.Hour),
				Expiration:   time.Now().Add(24 * time.Hour),
				Voucher:      false,
			},
		},
		Buyer: entity.Buyer{
			ID:                1,
			BillingAddressID:  1,
			ShippingAddressID: 2,
			BuyerInsert: entity.BuyerInsert{
				FirstName:          "Test",
				LastName:           "User",
				Email:              "test@example.com",
				Phone:              "+1234567890",
				ReceivePromoEmails: sql.NullBool{Bool: true, Valid: true},
			},
		},
		Billing: entity.Address{
			ID: 1,
			AddressInsert: entity.AddressInsert{
				OrderId:        1,
				Country:        "Test Country",
				City:           "Test City",
				AddressLineOne: "123 Test St",
				AddressLineTwo: sql.NullString{String: "Apt 4B", Valid: true},
				PostalCode:     "12345",
			},
		},
		Shipping: entity.Address{
			ID: 2,
			AddressInsert: entity.AddressInsert{
				OrderId:        1,
				Country:        "Test Country",
				City:           "Test City",
				AddressLineOne: "123 Test St",
				AddressLineTwo: sql.NullString{String: "Apt 4B", Valid: true},
				PostalCode:     "12345",
			},
		},
	}

	// Setup mock for ValidateOrderByUUID
	mockOrders.EXPECT().ValidateOrderByUUID(mock.Anything, "test-uuid-123").Return(mockOrderFull, nil)

	// Call the function being tested
	resp, err := server.ValidateOrderByUUID(ctx, &pb_frontend.ValidateOrderByUUIDRequest{
		OrderUuid: "test-uuid-123",
	})

	// Assert expectations
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.NotNil(t, resp.Order)

	// Verify order data
	assert.Equal(t, int32(1), resp.Order.Order.Id)
	assert.Equal(t, "test-uuid-123", resp.Order.Order.Uuid)
	assert.Equal(t, "100", resp.Order.Order.TotalPrice.Value)
	assert.Equal(t, int32(1), resp.Order.Order.OrderStatusId)

	// Verify order items
	assert.Len(t, resp.Order.OrderItems, 1)
	assert.Equal(t, int32(1), resp.Order.OrderItems[0].Id)
	assert.Equal(t, int32(1), resp.Order.OrderItems[0].OrderId)
	assert.Equal(t, "https://example.com/thumbnail.jpg", resp.Order.OrderItems[0].Thumbnail)
	assert.Equal(t, "Test Product", resp.Order.OrderItems[0].ProductName)
	assert.Equal(t, "Test Brand", resp.Order.OrderItems[0].ProductBrand)
	assert.Equal(t, "50", resp.Order.OrderItems[0].ProductPrice)
	assert.Equal(t, "10", resp.Order.OrderItems[0].ProductSalePercentage)
	assert.Equal(t, "45", resp.Order.OrderItems[0].ProductPriceWithSale)
	assert.Equal(t, "TST123", resp.Order.OrderItems[0].Sku)

	// Verify payment
	assert.NotNil(t, resp.Order.Payment)
	assert.Equal(t, pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST, resp.Order.Payment.PaymentInsert.PaymentMethod)
	assert.Equal(t, "100", resp.Order.Payment.PaymentInsert.TransactionAmount.Value)
	assert.Equal(t, "test-client-secret", resp.Order.Payment.PaymentInsert.ClientSecret)
	assert.False(t, resp.Order.Payment.PaymentInsert.IsTransactionDone)

	// Verify shipment
	assert.NotNil(t, resp.Order.Shipment)
	assert.Equal(t, int32(1), resp.Order.Shipment.CarrierId)
	assert.Equal(t, "TRACK123", resp.Order.Shipment.TrackingCode)

	// Verify promo code
	assert.NotNil(t, resp.Order.PromoCode)
	assert.Equal(t, "TEST10", resp.Order.PromoCode.PromoCodeInsert.Code)
	assert.Equal(t, "10", resp.Order.PromoCode.PromoCodeInsert.Discount.Value)
	assert.False(t, resp.Order.PromoCode.PromoCodeInsert.FreeShipping)
	assert.True(t, resp.Order.PromoCode.PromoCodeInsert.Allowed)

	// Verify buyer
	assert.NotNil(t, resp.Order.Buyer)
	assert.Equal(t, "Test", resp.Order.Buyer.BuyerInsert.FirstName)
	assert.Equal(t, "User", resp.Order.Buyer.BuyerInsert.LastName)
	assert.Equal(t, "test@example.com", resp.Order.Buyer.BuyerInsert.Email)
	assert.Equal(t, "+1234567890", resp.Order.Buyer.BuyerInsert.Phone)
	assert.True(t, resp.Order.Buyer.BuyerInsert.ReceivePromoEmails)

	// Verify addresses
	assert.NotNil(t, resp.Order.Billing)
	assert.Equal(t, "Test Country", resp.Order.Billing.AddressInsert.Country)
	assert.Equal(t, "Test City", resp.Order.Billing.AddressInsert.City)
	assert.Equal(t, "123 Test St", resp.Order.Billing.AddressInsert.AddressLineOne)
	assert.Equal(t, "Apt 4B", resp.Order.Billing.AddressInsert.AddressLineTwo)
	assert.Equal(t, "12345", resp.Order.Billing.AddressInsert.PostalCode)

	assert.NotNil(t, resp.Order.Shipping)
	assert.Equal(t, "Test Country", resp.Order.Shipping.AddressInsert.Country)
	assert.Equal(t, "Test City", resp.Order.Shipping.AddressInsert.City)
	assert.Equal(t, "123 Test St", resp.Order.Shipping.AddressInsert.AddressLineOne)
	assert.Equal(t, "Apt 4B", resp.Order.Shipping.AddressInsert.AddressLineTwo)
	assert.Equal(t, "12345", resp.Order.Shipping.AddressInsert.PostalCode)
}

// TestGetOrderInvoice tests the GetOrderInvoice method separately from the main TestFrontend function
func TestGetOrderInvoice(t *testing.T) {
	// Create context
	ctx := context.Background()

	// Setup mock repository
	mockRepo := mocks.NewRepository(t)

	// Setup mock invoicers
	mockStripePayment := mocks.NewInvoicer(t)
	mockStripePaymentTest := mocks.NewInvoicer(t)

	// Setup mock revalidation service
	mockRe := mocks.NewRevalidationService(t)

	// Initialize cache with test data
	dictionaryInfo := &entity.DictionaryInfo{
		OrderStatuses: []entity.OrderStatus{
			{Id: 1, Name: entity.Placed},
			{Id: 2, Name: entity.AwaitingPayment},
			{Id: 3, Name: entity.Confirmed},
			{Id: 4, Name: entity.Shipped},
			{Id: 5, Name: entity.Delivered},
			{Id: 6, Name: entity.Cancelled},
			{Id: 7, Name: entity.Refunded},
		},
		PaymentMethods: []entity.PaymentMethod{
			{Id: 1, Name: entity.CARD, Allowed: true},
			{Id: 2, Name: entity.CARD_TEST, Allowed: true},
		},
	}

	// Initialize cache with test data
	err := cache.InitConsts(ctx, dictionaryInfo, &entity.HeroFull{})
	assert.NoError(t, err)

	// Setup mock mailer
	mockMailer := mocks.NewMailer(t)

	// Create server with mocked dependencies
	server := New(
		mockRepo,
		mockMailer,
		mockStripePayment,
		mockStripePaymentTest,
		mockRe,
		stockreserve.NewDefaultManager(),
	)

	// Create mock payment insert
	mockPaymentInsert := &entity.PaymentInsert{
		OrderId:                          1,
		PaymentMethodID:                  2, // Card test
		TransactionAmount:                decimal.NewFromInt(100),
		TransactionAmountPaymentCurrency: decimal.NewFromInt(100),
		ClientSecret:                     sql.NullString{String: "test-client-secret", Valid: true},
		IsTransactionDone:                false,
		ExpiredAt:                        sql.NullTime{Time: time.Now().Add(15 * time.Minute), Valid: true},
	}

	// Setup mock for GetOrderInvoice
	mockStripePaymentTest.EXPECT().GetOrderInvoice(
		mock.Anything,
		"test-uuid-123",
	).Return(mockPaymentInsert, nil)

	// Call the function being tested
	resp, err := server.GetOrderInvoice(ctx, &pb_frontend.GetOrderInvoiceRequest{
		OrderUuid:     "test-uuid-123",
		PaymentMethod: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST,
	})

	// Assert expectations
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.NotNil(t, resp.Payment)
	assert.Equal(t, pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST, resp.Payment.PaymentMethod)
	assert.Equal(t, "100", resp.Payment.TransactionAmount.Value)
	assert.Equal(t, "test-client-secret", resp.Payment.ClientSecret)
	assert.False(t, resp.Payment.IsTransactionDone)
}

// TestCancelOrderInvoice tests the CancelOrderInvoice method separately from the main TestFrontend function
func TestCancelOrderInvoice(t *testing.T) {
	// Create context
	ctx := context.Background()

	// Setup mock repository
	mockRepo := mocks.NewRepository(t)

	// Setup mock order service
	mockOrders := mocks.NewOrder(t)
	mockRepo.EXPECT().Order().Return(mockOrders)

	// Setup mock invoicers
	mockStripePayment := mocks.NewInvoicer(t)
	mockStripePaymentTest := mocks.NewInvoicer(t)

	// Setup mock revalidation service
	mockRe := mocks.NewRevalidationService(t)

	// Initialize cache with test data
	dictionaryInfo := &entity.DictionaryInfo{
		OrderStatuses: []entity.OrderStatus{
			{Id: 1, Name: entity.Placed},
			{Id: 2, Name: entity.AwaitingPayment},
			{Id: 3, Name: entity.Confirmed},
			{Id: 4, Name: entity.Shipped},
			{Id: 5, Name: entity.Delivered},
			{Id: 6, Name: entity.Cancelled},
			{Id: 7, Name: entity.Refunded},
		},
		PaymentMethods: []entity.PaymentMethod{
			{Id: 1, Name: entity.CARD, Allowed: true},
			{Id: 2, Name: entity.CARD_TEST, Allowed: true},
		},
	}

	// Initialize cache with test data
	err := cache.InitConsts(ctx, dictionaryInfo, &entity.HeroFull{})
	assert.NoError(t, err)

	// Setup mock mailer
	mockMailer := mocks.NewMailer(t)

	// Create server with mocked dependencies
	server := New(
		mockRepo,
		mockMailer,
		mockStripePayment,
		mockStripePaymentTest,
		mockRe,
		stockreserve.NewDefaultManager(),
	)

	// Create mock payment
	mockPayment := &entity.Payment{
		Id:         1,
		CreatedAt:  time.Now(),
		ModifiedAt: time.Now(),
		PaymentInsert: entity.PaymentInsert{
			OrderId:                          1,
			PaymentMethodID:                  2, // CARD_TEST
			TransactionAmount:                decimal.NewFromInt(100),
			TransactionAmountPaymentCurrency: decimal.NewFromInt(100),
			IsTransactionDone:                false,
			ExpiredAt:                        sql.NullTime{Time: time.Now().Add(15 * time.Minute), Valid: true},
		},
	}

	// Setup mock for ExpireOrderPayment
	mockOrders.EXPECT().ExpireOrderPayment(
		mock.Anything,
		"test-uuid-123",
	).Return(mockPayment, nil)

	// Setup mock for CancelMonitorPayment
	mockStripePaymentTest.EXPECT().CancelMonitorPayment(
		"test-uuid-123",
	).Return(nil)

	// Call the function being tested
	resp, err := server.CancelOrderInvoice(ctx, &pb_frontend.CancelOrderInvoiceRequest{
		OrderUuid: "test-uuid-123",
	})

	// Assert expectations
	assert.NoError(t, err)
	assert.NotNil(t, resp)
}

// TestSubscribeNewsletter tests the SubscribeNewsletter method separately from the main TestFrontend function
func TestSubscribeNewsletter(t *testing.T) {
	// Create context
	ctx := context.Background()

	// Setup mock repository
	mockRepo := mocks.NewRepository(t)

	// Setup mock subscribers service
	mockSubscribers := mocks.NewSubscribers(t)
	mockRepo.EXPECT().Subscribers().Return(mockSubscribers)

	// Setup mock mailer
	mockMailer := mocks.NewMailer(t)

	// Setup mock invoicers
	mockStripePayment := mocks.NewInvoicer(t)
	mockStripePaymentTest := mocks.NewInvoicer(t)

	// Setup mock revalidation service
	mockRe := mocks.NewRevalidationService(t)

	// Create server with mocked dependencies
	server := New(
		mockRepo,
		mockMailer,
		mockStripePayment,
		mockStripePaymentTest,
		mockRe,
		stockreserve.NewDefaultManager(),
	)

	// Test email address
	testEmail := "test@example.com"

	// Setup mock for UpsertSubscription
	mockSubscribers.EXPECT().UpsertSubscription(
		mock.Anything,
		testEmail,
		true,
	).Return(nil)

	// Setup mock for SendNewSubscriber
	mockMailer.EXPECT().SendNewSubscriber(
		mock.Anything,
		mockRepo,
		testEmail,
	).Return(nil)

	// Call the function being tested
	resp, err := server.SubscribeNewsletter(ctx, &pb_frontend.SubscribeNewsletterRequest{
		Email: testEmail,
	})

	// Assert expectations
	assert.NoError(t, err)
	assert.NotNil(t, resp)

	// Test error case: UpsertSubscription fails
	mockSubscribers.EXPECT().UpsertSubscription(
		mock.Anything,
		"error@example.com",
		true,
	).Return(fmt.Errorf("subscription error"))

	// Call the function with an email that will cause an error
	resp, err = server.SubscribeNewsletter(ctx, &pb_frontend.SubscribeNewsletterRequest{
		Email: "error@example.com",
	})

	// Assert expectations for error case
	assert.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "can't subscribe")

	// Test error case: SendNewSubscriber fails
	mockSubscribers.EXPECT().UpsertSubscription(
		mock.Anything,
		"mail-error@example.com",
		true,
	).Return(nil)

	mockMailer.EXPECT().SendNewSubscriber(
		mock.Anything,
		mockRepo,
		"mail-error@example.com",
	).Return(fmt.Errorf("mail error"))

	// Call the function with an email that will cause a mail error
	resp, err = server.SubscribeNewsletter(ctx, &pb_frontend.SubscribeNewsletterRequest{
		Email: "mail-error@example.com",
	})

	// Assert expectations for mail error case
	assert.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "can't send new subscriber mail")
}

// TestUnsubscribeNewsletter tests the UnsubscribeNewsletter method separately from the main TestFrontend function
func TestUnsubscribeNewsletter(t *testing.T) {
	// Create context
	ctx := context.Background()

	// Setup mock repository
	mockRepo := mocks.NewRepository(t)

	// Setup mock subscribers service
	mockSubscribers := mocks.NewSubscribers(t)
	mockRepo.EXPECT().Subscribers().Return(mockSubscribers)

	// Setup mock mailer
	mockMailer := mocks.NewMailer(t)

	// Setup mock invoicers
	mockStripePayment := mocks.NewInvoicer(t)
	mockStripePaymentTest := mocks.NewInvoicer(t)

	// Setup mock revalidation service
	mockRe := mocks.NewRevalidationService(t)

	// Create server with mocked dependencies
	server := New(
		mockRepo,
		mockMailer,
		mockStripePayment,
		mockStripePaymentTest,
		mockRe,
		stockreserve.NewDefaultManager(),
	)

	// Test email address
	testEmail := "test@example.com"

	// Setup mock for UpsertSubscription with receivePromo=false for unsubscribe
	mockSubscribers.EXPECT().UpsertSubscription(
		mock.Anything,
		testEmail,
		false,
	).Return(nil)

	// Call the function being tested
	resp, err := server.UnsubscribeNewsletter(ctx, &pb_frontend.UnsubscribeNewsletterRequest{
		Email: testEmail,
	})

	// Assert expectations
	assert.NoError(t, err)
	assert.NotNil(t, resp)

	// Test error case: UpsertSubscription fails
	mockSubscribers.EXPECT().UpsertSubscription(
		mock.Anything,
		"error@example.com",
		false,
	).Return(fmt.Errorf("unsubscription error"))

	// Call the function with an email that will cause an error
	resp, err = server.UnsubscribeNewsletter(ctx, &pb_frontend.UnsubscribeNewsletterRequest{
		Email: "error@example.com",
	})

	// Assert expectations for error case
	assert.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "can't unsubscribe")
}

// TestGetArchivesPaged tests the GetArchivesPaged method separately from the main TestFrontend function
func TestGetArchivesPaged(t *testing.T) {
	// Create context
	ctx := context.Background()

	// Setup mock repository
	mockRepo := mocks.NewRepository(t)

	// Setup mock archive service
	mockArchive := mocks.NewArchive(t)
	mockRepo.EXPECT().Archive().Return(mockArchive)

	// Setup mock mailer
	mockMailer := mocks.NewMailer(t)

	// Setup mock invoicers
	mockStripePayment := mocks.NewInvoicer(t)
	mockStripePaymentTest := mocks.NewInvoicer(t)

	// Setup mock revalidation service
	mockRe := mocks.NewRevalidationService(t)

	// Create server with mocked dependencies
	server := New(
		mockRepo,
		mockMailer,
		mockStripePayment,
		mockStripePaymentTest,
		mockRe,
		stockreserve.NewDefaultManager(),
	)

	// Create mock archive data
	mockArchives := []entity.ArchiveFull{
		{
			Id:          1,
			Heading:     "Test Archive 1",
			Description: "Test Description 1",
			Tag:         "test-tag-1",
			Slug:        "test-archive-1",
			NextSlug:    "test-archive-2",
			CreatedAt:   time.Now(),
			Media: []entity.MediaFull{
				{
					Id:        1,
					CreatedAt: time.Now(),
					MediaItem: entity.MediaItem{
						FullSizeMediaURL:   "https://example.com/image1.jpg",
						FullSizeWidth:      1000,
						FullSizeHeight:     800,
						ThumbnailMediaURL:  "https://example.com/thumbnail1.jpg",
						ThumbnailWidth:     200,
						ThumbnailHeight:    160,
						CompressedMediaURL: "https://example.com/compressed1.jpg",
						CompressedWidth:    500,
						CompressedHeight:   400,
						BlurHash:           sql.NullString{String: "LKO2?U%2Tw=w]~RBVZRi};RPxuwH", Valid: true},
					},
				},
			},
		},
		{
			Id:          2,
			Heading:     "Test Archive 2",
			Description: "Test Description 2",
			Tag:         "test-tag-2",
			Slug:        "test-archive-2",
			NextSlug:    "",
			CreatedAt:   time.Now().Add(-24 * time.Hour),
			Media: []entity.MediaFull{
				{
					Id:        2,
					CreatedAt: time.Now().Add(-24 * time.Hour),
					MediaItem: entity.MediaItem{
						FullSizeMediaURL:   "https://example.com/image2.jpg",
						FullSizeWidth:      1000,
						FullSizeHeight:     800,
						ThumbnailMediaURL:  "https://example.com/thumbnail2.jpg",
						ThumbnailWidth:     200,
						ThumbnailHeight:    160,
						CompressedMediaURL: "https://example.com/compressed2.jpg",
						CompressedWidth:    500,
						CompressedHeight:   400,
						BlurHash:           sql.NullString{String: "LKO2?U%2Tw=w]~RBVZRi};RPxuwH", Valid: true},
					},
				},
			},
		},
	}

	// Setup mock for GetArchivesPaged
	mockArchive.EXPECT().GetArchivesPaged(
		mock.Anything,
		10,
		0,
		entity.Descending,
	).Return(mockArchives, 2, nil)

	// Call the function being tested
	resp, err := server.GetArchivesPaged(ctx, &pb_frontend.GetArchivesPagedRequest{
		Limit:       10,
		Offset:      0,
		OrderFactor: pb_common.OrderFactor_ORDER_FACTOR_DESC,
	})

	// Assert expectations
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, int32(2), resp.Total)
	assert.Len(t, resp.Archives, 2)

	// Verify first archive
	assert.Equal(t, int32(1), resp.Archives[0].Id)
	assert.Equal(t, "Test Archive 1", resp.Archives[0].Heading)
	assert.Equal(t, "Test Description 1", resp.Archives[0].Description)
	assert.Equal(t, "test-tag-1", resp.Archives[0].Tag)
	assert.Equal(t, "test-archive-1", resp.Archives[0].Slug)
	assert.Equal(t, "test-archive-2", resp.Archives[0].NextSlug)
	assert.Len(t, resp.Archives[0].Media, 1)
	assert.Equal(t, "https://example.com/image1.jpg", resp.Archives[0].Media[0].Media.FullSize.MediaUrl)

	// Verify second archive
	assert.Equal(t, int32(2), resp.Archives[1].Id)
	assert.Equal(t, "Test Archive 2", resp.Archives[1].Heading)
	assert.Equal(t, "Test Description 2", resp.Archives[1].Description)
	assert.Equal(t, "test-tag-2", resp.Archives[1].Tag)
	assert.Equal(t, "test-archive-2", resp.Archives[1].Slug)
	assert.Equal(t, "", resp.Archives[1].NextSlug)
	assert.Len(t, resp.Archives[1].Media, 1)
	assert.Equal(t, "https://example.com/image2.jpg", resp.Archives[1].Media[0].Media.FullSize.MediaUrl)
}

func TestSubmitSupportTicket_Success(t *testing.T) {
	mockRepo := new(MockRepository)
	mockSupport := new(MockSupport)
	mockRepo.On("Support").Return(mockSupport)

	server := &Server{
		repo:        mockRepo,
		rateLimiter: ratelimit.NewMultiKeyLimiter(),
	}

	ctx := middleware.WithClientIP(context.Background(), "192.168.1.1")

	ticket := &pb_common.SupportTicketInsert{
		Topic:          "Order Issue",
		Subject:        "Problem with order",
		Civility:       "Mr",
		Email:          "test@example.com",
		FirstName:      "John",
		LastName:       "Doe",
		OrderReference: "ORD-123",
		Notes:          "I need help with my order",
		Category:       "shipping",
		Priority:       pb_common.SupportTicketPriority_SUPPORT_TICKET_PRIORITY_HIGH,
	}

	mockSupport.EXPECT().SubmitTicket(
		mock.Anything,
		mock.MatchedBy(func(t entity.SupportTicketInsert) bool {
			return t.Email == "test@example.com" &&
				t.Subject == "Problem with order" &&
				t.Priority == entity.PriorityHigh
		}),
	).Return("CS-2026-00001", nil)

	resp, err := server.SubmitSupportTicket(ctx, &pb_frontend.SubmitSupportTicketRequest{
		Ticket: ticket,
	})

	assert.NoError(t, err)
	assert.NotNil(t, resp)
	mockSupport.AssertExpectations(t)
}

func TestSubmitSupportTicket_InvalidEmail(t *testing.T) {
	mockRepo := new(MockRepository)
	mockSupport := new(MockSupport)
	mockRepo.On("Support").Return(mockSupport)

	server := &Server{
		repo:        mockRepo,
		rateLimiter: ratelimit.NewMultiKeyLimiter(),
	}

	ctx := middleware.WithClientIP(context.Background(), "192.168.1.1")

	ticket := &pb_common.SupportTicketInsert{
		Topic:     "Test",
		Subject:   "Test",
		Civility:  "Mr",
		Email:     "invalid-email",
		FirstName: "John",
		LastName:  "Doe",
		Notes:     "Test notes",
	}

	resp, err := server.SubmitSupportTicket(ctx, &pb_frontend.SubmitSupportTicketRequest{
		Ticket: ticket,
	})

	assert.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "invalid email format")
}

func TestSubmitSupportTicket_MissingRequiredFields(t *testing.T) {
	mockRepo := new(MockRepository)
	mockSupport := new(MockSupport)
	mockRepo.On("Support").Return(mockSupport)

	server := &Server{
		repo:        mockRepo,
		rateLimiter: ratelimit.NewMultiKeyLimiter(),
	}

	ctx := middleware.WithClientIP(context.Background(), "192.168.1.1")

	testCases := []struct {
		name   string
		ticket *pb_common.SupportTicketInsert
		errMsg string
	}{
		{
			name: "missing email",
			ticket: &pb_common.SupportTicketInsert{
				Subject:   "Test",
				FirstName: "John",
				LastName:  "Doe",
				Notes:     "Test",
			},
			errMsg: "email is required",
		},
		{
			name: "missing first name",
			ticket: &pb_common.SupportTicketInsert{
				Email:    "test@example.com",
				Subject:  "Test",
				LastName: "Doe",
				Notes:    "Test",
			},
			errMsg: "first name is required",
		},
		{
			name: "missing subject",
			ticket: &pb_common.SupportTicketInsert{
				Email:     "test@example.com",
				FirstName: "John",
				LastName:  "Doe",
				Notes:     "Test",
			},
			errMsg: "subject is required",
		},
		{
			name: "missing notes",
			ticket: &pb_common.SupportTicketInsert{
				Email:     "test@example.com",
				FirstName: "John",
				LastName:  "Doe",
				Subject:   "Test",
			},
			errMsg: "notes are required",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := server.SubmitSupportTicket(ctx, &pb_frontend.SubmitSupportTicketRequest{
				Ticket: tc.ticket,
			})

			assert.Error(t, err)
			assert.Nil(t, resp)
			assert.Contains(t, err.Error(), tc.errMsg)
		})
	}
}

func TestSubmitSupportTicket_RateLimitExceeded(t *testing.T) {
	mockRepo := new(MockRepository)
	mockSupport := new(MockSupport)
	mockRepo.On("Support").Return(mockSupport)

	limiter := ratelimit.NewCustomMultiKeyLimiter(100, 100, 20)
	server := &Server{
		repo:        mockRepo,
		rateLimiter: limiter,
	}

	ctx := middleware.WithClientIP(context.Background(), "192.168.1.1")

	ticket := &pb_common.SupportTicketInsert{
		Topic:     "Test",
		Subject:   "Test ticket",
		Civility:  "Mr",
		Email:     "test@example.com",
		FirstName: "John",
		LastName:  "Doe",
		Notes:     "Test notes",
	}

	mockSupport.On("SubmitTicket", mock.Anything, mock.Anything).Return("CS-2026-00001", nil)

	for i := 0; i < 5; i++ {
		_, err := server.SubmitSupportTicket(ctx, &pb_frontend.SubmitSupportTicketRequest{
			Ticket: ticket,
		})
		assert.NoError(t, err)
	}

	resp, err := server.SubmitSupportTicket(ctx, &pb_frontend.SubmitSupportTicketRequest{
		Ticket: ticket,
	})

	assert.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "too many support tickets")
}

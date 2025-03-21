package stripe

// import (
// 	"database/sql"
// 	"testing"

// 	"github.com/google/uuid"
// 	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
// 	"github.com/jekabolt/grbpwr-manager/internal/dto"
// 	"github.com/jekabolt/grbpwr-manager/internal/entity"
// 	"github.com/shopspring/decimal"
// 	"github.com/stretchr/testify/assert"
// )

// func TestCreatePaymentIntent(t *testing.T) {
// 	// Setup mock RatesService

// 	config := &Config{
// 		SecretKey: "sk_test_",
// 		PubKey:    "pk_test_",
// 	}

// 	mockRatesService := mocks.NewRatesService(t)

// 	// Create Processor with the mock RatesService
// 	processor, err := New(config, mockRatesService)
// 	assert.NoError(t, err)

// 	// Mocking the response of ConvertFromBaseCurrency
// 	mockRatesService.EXPECT().ConvertFromBaseCurrency(dto.CurrencyTicker("USD"), decimal.NewFromFloat(20.00)).Return(decimal.NewFromFloat(20.00), nil)

// 	// Create a sample order
// 	order := entity.OrderFull{
// 		Order: entity.Order{
// 			UUID:       uuid.New().String(),
// 			TotalPrice: decimal.NewFromFloat(20.00),
// 		},
// 		Buyer: entity.Buyer{
// 			BuyerInsert: entity.BuyerInsert{
// 				Email:     "buyer@example.com",
// 				FirstName: "test",
// 				LastName:  "test",
// 			},
// 		},
// 		Shipping: entity.Address{
// 			AddressInsert: entity.AddressInsert{
// 				City:           "New York",
// 				Country:        "US",
// 				AddressLineOne: "123 Main St",
// 				AddressLineTwo: sql.NullString{String: "Apt 4", Valid: true},
// 				PostalCode:     "10001",
// 				State:          sql.NullString{String: "NY", Valid: true},
// 			},
// 		},
// 	}

// 	// Call createPaymentIntent
// 	pi, err := processor.CreatePaymentIntent(order)

// 	// Assertions
// 	assert.NoError(t, err)

// 	t.Logf("PaymentIntent: %+v ", pi.ClientSecret)

// 	// assert.NotNil(t, pi)
// 	// assert.Equal(t, int64(2000), pi.Amount) // Amount should be in cents
// 	// assert.Equal(t, "usd", *pi.Currency)
// 	// assert.Equal(t, "card", pi.PaymentMethodTypes[0])
// 	// assert.Equal(t, "buyer@example.com", *pi.ReceiptEmail)
// 	// assert.Equal(t, fmt.Sprintf("order #%s", order.Order.UUID), *pi.Description)
// 	// assert.Equal(t, "John Doe", *pi.Shipping.Name)
// 	// assert.Equal(t, "New York", *pi.Shipping.Address.City)
// }

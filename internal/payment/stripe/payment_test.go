package stripe

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPaymentMethodTypesForCurrency(t *testing.T) {
	// KRW: card, kr_card, apple_pay
	krw := PaymentMethodTypesForCurrency("KRW")
	assert.ElementsMatch(t, []string{"card", "kr_card", "apple_pay"}, krw)

	// EUR: card + bancontact, ideal, eps, p24, sepa_debit, multibanco, klarna, paypal, alipay, wechat_pay, afterpay_clearpay
	eur := PaymentMethodTypesForCurrency("eur")
	assert.Contains(t, eur, "card")
	assert.Contains(t, eur, "klarna")
	assert.Contains(t, eur, "paypal")
	assert.Contains(t, eur, "alipay")
	assert.Contains(t, eur, "wechat_pay")
	assert.Contains(t, eur, "bancontact")
	assert.Contains(t, eur, "ideal")
	assert.Contains(t, eur, "sepa_debit")

	// USD: card + affirm, afterpay_clearpay, klarna, paypal, alipay, wechat_pay, acss_debit, us_bank_account
	usd := PaymentMethodTypesForCurrency("usd")
	assert.Contains(t, usd, "card")
	assert.Contains(t, usd, "klarna")
	assert.Contains(t, usd, "paypal")
	assert.Contains(t, usd, "alipay")
	assert.Contains(t, usd, "wechat_pay")
	assert.Contains(t, usd, "affirm")
	assert.Contains(t, usd, "us_bank_account")

	// HKD: card, paypal, alipay, wechat_pay, apple_pay (no Klarna)
	hkd := PaymentMethodTypesForCurrency("HKD")
	assert.ElementsMatch(t, []string{"card", "paypal", "alipay", "wechat_pay", "apple_pay"}, hkd)

	// CNY: card, alipay, wechat_pay, apple_pay
	cny := PaymentMethodTypesForCurrency("CNY")
	assert.ElementsMatch(t, []string{"card", "alipay", "wechat_pay", "apple_pay"}, cny)

	// JPY: card, alipay, wechat_pay, konbini, paypay, apple_pay
	jpy := PaymentMethodTypesForCurrency("JPY")
	assert.ElementsMatch(t, []string{"card", "alipay", "wechat_pay", "konbini", "paypay", "apple_pay"}, jpy)

	// SGD: card, paypal, alipay, wechat_pay, grabpay, paynow
	sgd := PaymentMethodTypesForCurrency("SGD")
	assert.Contains(t, sgd, "card")
	assert.Contains(t, sgd, "paypal")
	assert.Contains(t, sgd, "grabpay")
	assert.Contains(t, sgd, "paynow")
}

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

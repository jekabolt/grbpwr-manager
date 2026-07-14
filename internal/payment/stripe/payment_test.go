package stripe

import (
	"context"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestUpdateOrderAsPaidRejectsUnderpayment verifies the amount-tamper guard:
// when Stripe received less than the order total, the order is NOT marked paid
// (OrderPaymentDone is never called), ErrUnderpaid is returned, and the order is
// flagged for manual review via an order comment.
func TestUpdateOrderAsPaidRejectsUnderpayment(t *testing.T) {
	mockOrders := mocks.NewMockOrder(t)
	mockRepo := mocks.NewMockRepository(t)
	mockRepo.EXPECT().Order().Return(mockOrders)
	mockOrders.EXPECT().
		AddOrderComment(mock.Anything, "ord-underpaid", mock.AnythingOfType("string")).
		Return(nil)
	// OrderPaymentDone must NOT be set: if updateOrderAsPaid called it, the mock
	// would fail on an unexpected call.

	p := &Processor{rep: mockRepo}
	payment := entity.Payment{PaymentInsert: entity.PaymentInsert{
		TransactionAmountPaymentCurrency: decimal.RequireFromString("123.45"),
	}}
	received := decimal.RequireFromString("100.00")

	err := p.updateOrderAsPaid(context.Background(), mockRepo, "ord-underpaid", payment, received)
	assert.ErrorIs(t, err, ErrUnderpaid)
}

// TestUpdateOrderAsPaidAllowsFullPayment verifies that a payment covering the
// order total passes the guard and proceeds to OrderPaymentDone (here stubbed to
// error so the test stays light), i.e. it does not return ErrUnderpaid.
func TestUpdateOrderAsPaidAllowsFullPayment(t *testing.T) {
	mockOrders := mocks.NewMockOrder(t)
	mockRepo := mocks.NewMockRepository(t)
	mockRepo.EXPECT().Order().Return(mockOrders)
	mockOrders.EXPECT().
		OrderPaymentDone(mock.Anything, "ord-paid", mock.Anything).
		Return(false, assert.AnError)

	p := &Processor{rep: mockRepo}
	payment := entity.Payment{PaymentInsert: entity.PaymentInsert{
		TransactionAmountPaymentCurrency: decimal.RequireFromString("123.45"),
	}}
	received := decimal.RequireFromString("123.45")

	err := p.updateOrderAsPaid(context.Background(), mockRepo, "ord-paid", payment, received)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrUnderpaid)
}

// TestTopUpSettledBaseStopsOnCancelledParent verifies the background settled top-up is
// shutdown-safe: when the processor-wide parent context is already cancelled (as
// StopAllMonitors does at shutdown before the DB is closed), the goroutine returns
// immediately without hitting Stripe or the DB. The Processor here has a nil stripeClient
// and nil rep, so any call past the first context check would panic — reaching monWg.Done
// (awaited below) proves it short-circuited on ctx.Done before touching either.
func TestTopUpSettledBaseStopsOnCancelledParent(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel() // simulate shutdown: monParentCtx already cancelled

	p := &Processor{monParentCtx: parent}
	p.monWg.Add(1) // mirror the production launch site (Add before `go`)

	done := make(chan struct{})
	go func() {
		p.topUpSettledBase("ord-x", "pi_x_secret_y")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("topUpSettledBase did not return promptly on cancelled parent context")
	}
	p.monWg.Wait() // Done ran: the goroutine is accounted for at shutdown
}

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

package adyen

// import (
// 	"fmt"

// 	"github.com/jekabolt/grbpwr-manager/internal/dependency"
// 	"github.com/jekabolt/grbpwr-manager/internal/entity"
// 	"github.com/shopspring/decimal"
// 	"github.com/stripe/stripe-go/v72"
// 	"github.com/stripe/stripe-go/v72/paymentintent"
// )

// type Config struct {
// 	SecretKey     string `mapstructure:"secret_key"`
// 	WebhookSecret string `mapstructure:"webhook_secret"`
// }

// type Processor struct {
// 	c      *Config
// 	pm     *entity.PaymentMethod
// 	mailer dependency.Mailer
// 	rates  dependency.RatesService
// }

// func New(c *Config, rep dependency.Repository, m dependency.Mailer, r dependency.RatesService, pmn entity.PaymentMethodName) (dependency.Invoice, error) {
// 	pm, ok := rep.Cache().GetPaymentMethodByName(pmn)
// 	if !ok {
// 		return nil, fmt.Errorf("payment method not found")
// 	}

// 	p := &Processor{
// 		c:      c,
// 		pm:     &pm,
// 		mailer: m,
// 		rates:  r,
// 	}

// 	return p, nil

// }

// func createPaymentIntent(amount decimal.Decimal, currency string) (*stripe.PaymentIntent, error) {
// 	params := &stripe.PaymentIntentParams{
// 		Amount:   stripe.Int64(amount.IntPart()),
// 		Currency: stripe.String(currency),
// 		PaymentMethodTypes: stripe.StringSlice([]string{
// 			"card",
// 			"apple_pay",
// 		}),
// 	}

// 	return paymentintent.New(params)
// }

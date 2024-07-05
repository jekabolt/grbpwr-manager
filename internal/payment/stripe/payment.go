package stripe

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/paymentintent"
)

type Config struct {
	SecretKey          string             `mapstructure:"secret_key"`
	PubKey             string             `mapstructure:"pub_key"`
	BaseCurrency       dto.CurrencyTicker `mapstructure:"default_currency"`
	PaymentMethodTypes []string           `mapstructure:"payment_method_types"`
}

type Processor struct {
	c            *Config
	baseCurrency string
	rs           dependency.RatesService
}

func New(c *Config, rs dependency.RatesService) (*Processor, error) {

	ok := dto.VerifyCurrencyTicker(c.BaseCurrency.String())
	if !ok {
		return nil, fmt.Errorf("invalid default currency: %s", c.BaseCurrency)
	}

	return &Processor{
		c:            c,
		baseCurrency: c.BaseCurrency.String(),
		rs:           rs,
	}, nil
}

// createPaymentIntent creates a PaymentIntent with the specified amount, currency, and payment method types
func (p *Processor) createPaymentIntent(order entity.OrderFull) (*stripe.PaymentIntent, error) {

	// Convert the order total to the stripe payment base currency
	total, err := p.rs.ConvertFromBaseCurrency(p.c.BaseCurrency, order.Order.TotalPrice)
	if err != nil {
		return nil, err
	}

	// Calculate the order amount in cents
	amountCents := total.Mul(decimal.NewFromInt(100)).IntPart()

	params := &stripe.PaymentIntentParams{
		Amount:             stripe.Int64(amountCents),                                 // Amount to charge in the smallest currency unit (e.g., cents for USD)
		Currency:           stripe.String(p.c.BaseCurrency.String()),                  // Currency in which to charge the payment
		PaymentMethodTypes: stripe.StringSlice(p.c.PaymentMethodTypes),                // Types of payment methods (e.g., "card")
		ReceiptEmail:       stripe.String(order.Buyer.Email),                          // Email to send the receipt to
		Description:        stripe.String(fmt.Sprintf("order #%s", order.Order.UUID)), // Description of the payment
		Metadata: map[string]string{ // Metadata for additional information
			"order_id": order.Order.UUID,
		},
		Shipping: &stripe.ShippingDetailsParams{ // Shipping details
			Address: &stripe.AddressParams{
				Line1:      stripe.String(fmt.Sprintf("%s %s apt %s", order.Shipping.Street, order.Shipping.HouseNumber, order.Shipping.ApartmentNumber)),
				City:       stripe.String(order.Shipping.City),
				State:      stripe.String(order.Shipping.State),
				PostalCode: stripe.String(order.Shipping.PostalCode),
				Country:    stripe.String(order.Shipping.Country),
			},
			Name: stripe.String(fmt.Sprintf("%s %s", order.Buyer.FirstName, order.Buyer.LastName)),
		},
	}

	// Create the PaymentIntent
	pi, err := paymentintent.New(params)
	if err != nil {
		return nil, err
	}

	return pi, nil
}

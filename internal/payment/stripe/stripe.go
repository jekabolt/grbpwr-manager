package stripe

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stripe/stripe-go/v79"
)

// createPaymentIntent creates a PaymentIntent with the specified amount, currency, and payment method types
func (p *Processor) createPaymentIntent(order entity.OrderFull) (*stripe.PaymentIntent, error) {

	// Convert the order total to the stripe payment base currency
	total, err := p.rates.ConvertFromBaseCurrency(p.baseCurrency, order.Order.TotalPrice)
	if err != nil {
		return nil, fmt.Errorf("failed to convert order total to base currency: %v", err)
	}

	// Calculate the order amount in cents
	amountCents := total.Mul(decimal.NewFromInt(100)).IntPart()

	params := &stripe.PaymentIntentParams{
		Amount:             stripe.Int64(amountCents),                                 // Amount to charge in the smallest currency unit (e.g., cents for USD)
		Currency:           stripe.String(p.baseCurrency.String()),                    // Currency in which to charge the payment
		PaymentMethodTypes: stripe.StringSlice(PaymentMethodTypes),                    // Types of payment methods (e.g., "card")
		ReceiptEmail:       stripe.String(order.Buyer.Email),                          // Email to send the receipt to
		Description:        stripe.String(fmt.Sprintf("order #%s", order.Order.UUID)), // Description of the payment
		Metadata: map[string]string{
			"order_id": order.Order.UUID,
		},
		Shipping: &stripe.ShippingDetailsParams{ // Shipping details
			Address: &stripe.AddressParams{
				City:       &order.Shipping.City,
				Country:    &order.Shipping.Country,
				Line1:      &order.Shipping.AddressLineOne,
				Line2:      &order.Shipping.AddressLineTwo.String,
				PostalCode: &order.Shipping.PostalCode,
				State:      &order.Shipping.State.String,
			},
			Name: stripe.String(fmt.Sprintf("%s %s", order.Buyer.FirstName, order.Buyer.LastName)),
		},
	}

	// Create the PaymentIntent
	pi, err := p.stripeClient.PaymentIntents.New(params)
	if err != nil {
		return nil, fmt.Errorf("failed to create PaymentIntent: %v", err)
	}

	return pi, nil
}

func (p *Processor) getPaymentIntent(paymentSecret string) (*stripe.PaymentIntent, error) {

	paymentIntentID := trimSecret(paymentSecret)

	pi, err := p.stripeClient.PaymentIntents.Get(paymentIntentID, nil)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve payment intent: %v", err)
	}

	return pi, nil
}
func (p *Processor) cancelPaymentIntent(paymentSecret string) (*stripe.PaymentIntent, error) {

	paymentIntentID := trimSecret(paymentSecret)

	pi, err := p.stripeClient.PaymentIntents.Cancel(paymentIntentID, nil)
	if err != nil {
		return nil, fmt.Errorf("unable to cancel payment intent: %v", err)
	}

	return pi, nil
}

func trimSecret(s string) string {
	// Find the index of "_secret_"
	index := strings.Index(s, "_secret_")
	if index == -1 {
		// If "_secret_" is not found, return the original string
		return s
	}
	// Return the substring up to the index of "_secret_"
	return s[:index]
}

package stripe

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stripe/stripe-go/v79"
)

// isZeroDecimalCurrency checks if a currency is zero-decimal (no cents/subunits)
// According to Stripe, these currencies don't have decimal places:
// BIF, CLP, DJF, GNF, JPY, KMF, KRW, MGA, PYG, RWF, UGX, VND, VUV, XAF, XOF, XPF
func isZeroDecimalCurrency(currency string) bool {
	zeroDecimalCurrencies := map[string]bool{
		"BIF": true, "CLP": true, "DJF": true, "GNF": true,
		"JPY": true, "KMF": true, "KRW": true, "MGA": true,
		"PYG": true, "RWF": true, "UGX": true, "VND": true,
		"VUV": true, "XAF": true, "XOF": true, "XPF": true,
	}
	return zeroDecimalCurrencies[strings.ToUpper(currency)]
}

// AmountToSmallestUnit converts an amount to the smallest currency unit for Stripe
// For zero-decimal currencies (like JPY, KRW), returns the amount as-is
// For other currencies, multiplies by 100 to convert to cents
func AmountToSmallestUnit(amount decimal.Decimal, currency string) int64 {
	if isZeroDecimalCurrency(currency) {
		return amount.IntPart()
	}
	return amount.Mul(decimal.NewFromInt(100)).IntPart()
}

// AmountFromSmallestUnit converts an amount from Stripe's smallest currency unit back to decimal
// For zero-decimal currencies (like JPY, KRW), returns the amount as-is
// For other currencies, divides by 100 to convert from cents
func AmountFromSmallestUnit(amount int64, currency string) decimal.Decimal {
	if isZeroDecimalCurrency(currency) {
		return decimal.NewFromInt(amount)
	}
	return decimal.NewFromInt(amount).Div(decimal.NewFromInt(100))
}

// amountToSmallestUnit converts an amount to the smallest currency unit for Stripe
// For zero-decimal currencies (like JPY, KRW), returns the amount as-is
// For other currencies, multiplies by 100 to convert to cents
func amountToSmallestUnit(amount decimal.Decimal, currency string) int64 {
	return AmountToSmallestUnit(amount, currency)
}

// amountFromSmallestUnit converts an amount from Stripe's smallest currency unit back to decimal
// For zero-decimal currencies (like JPY, KRW), returns the amount as-is
// For other currencies, divides by 100 to convert from cents
func amountFromSmallestUnit(amount int64, currency string) decimal.Decimal {
	return AmountFromSmallestUnit(amount, currency)
}

// createPaymentIntent creates a PaymentIntent with the specified amount, currency, and payment method types
func (p *Processor) createPaymentIntent(order entity.OrderFull) (*stripe.PaymentIntent, error) {
	// Use the order total directly - prices are already stored in the correct currency
	// Calculate the order amount in smallest currency unit (cents for most currencies, but not for zero-decimal currencies like JPY, KRW)
	amountCents := amountToSmallestUnit(order.Order.TotalPrice, order.Order.Currency)

	params := &stripe.PaymentIntentParams{
		Amount:   stripe.Int64(amountCents),
		Currency: stripe.String(order.Order.Currency),
		AutomaticPaymentMethods: &stripe.PaymentIntentAutomaticPaymentMethodsParams{
			Enabled: stripe.Bool(true),
		},
		ReceiptEmail: stripe.String(order.Buyer.Email),
		Description:  stripe.String(fmt.Sprintf("order %s", order.Order.UUID)),
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

// GetPaymentIntentByID retrieves a PaymentIntent by its ID
func (p *Processor) GetPaymentIntentByID(ctx context.Context, paymentIntentID string) (*stripe.PaymentIntent, error) {
	pi, err := p.stripeClient.PaymentIntents.Get(paymentIntentID, nil)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve payment intent: %v", err)
	}
	return pi, nil
}

// UpdatePaymentIntentAmount updates the amount of an existing PaymentIntent
func (p *Processor) UpdatePaymentIntentAmount(ctx context.Context, paymentIntentID string, amount decimal.Decimal, currency string) error {
	amountCents := AmountToSmallestUnit(amount, currency)

	params := &stripe.PaymentIntentParams{
		Amount:   stripe.Int64(amountCents),
		Currency: stripe.String(currency),
	}

	_, err := p.stripeClient.PaymentIntents.Update(paymentIntentID, params)
	if err != nil {
		return fmt.Errorf("failed to update PaymentIntent amount: %w", err)
	}

	return nil
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

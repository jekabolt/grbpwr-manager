package stripe

import (
	"context"
	"fmt"
	"strings"

	curr "github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stripe/stripe-go/v79"
)

// IsZeroDecimalCurrency checks if a currency is zero-decimal (no cents/subunits)
func IsZeroDecimalCurrency(c string) bool {
	return curr.IsZeroDecimal(c)
}

// AmountToSmallestUnit converts an amount to the smallest currency unit for Stripe
// For zero-decimal currencies (like JPY, KRW), rounds to whole units (no decimals)
// For other currencies, multiplies by 100 to convert to cents
func AmountToSmallestUnit(amount decimal.Decimal, c string) int64 {
	if curr.IsZeroDecimal(c) {
		return curr.Round(amount, c).IntPart()
	}
	return amount.Mul(decimal.NewFromInt(100)).IntPart()
}

// AmountFromSmallestUnit converts an amount from Stripe's smallest currency unit back to decimal
// For zero-decimal currencies (like JPY, KRW), returns the amount as-is
// For other currencies, divides by 100 to convert from cents
func AmountFromSmallestUnit(amount int64, c string) decimal.Decimal {
	if curr.IsZeroDecimal(c) {
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
	// Validate order total meets Stripe minimum (e.g. KRW >= 100)
	if err := dto.ValidatePriceMeetsMinimum(order.Order.TotalPrice, order.Order.Currency); err != nil {
		return nil, fmt.Errorf("order total below currency minimum: %w", err)
	}
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
		Shipping: &stripe.ShippingDetailsParams{
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
	params.SetIdempotencyKey(order.Order.UUID)

	// Create the PaymentIntent
	pi, err := p.stripeClient.PaymentIntents.New(params)
	if err != nil {
		return nil, fmt.Errorf("failed to create PaymentIntent: %v", err)
	}

	return pi, nil
}

func (p *Processor) getPaymentIntent(paymentSecret string) (*stripe.PaymentIntent, error) {
	return p.getPaymentIntentWithExpand(paymentSecret, nil)
}

// getPaymentIntentWithExpand retrieves a PaymentIntent, optionally expanding payment_method to get sub-type (apple_pay, klarna, etc.)
func (p *Processor) getPaymentIntentWithExpand(paymentSecret string, expand []string) (*stripe.PaymentIntent, error) {
	paymentIntentID := trimSecret(paymentSecret)

	var params *stripe.PaymentIntentParams
	if len(expand) > 0 {
		params = &stripe.PaymentIntentParams{}
		for _, e := range expand {
			params.AddExpand(e)
		}
	}

	pi, err := p.stripeClient.PaymentIntents.Get(paymentIntentID, params)
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

// Refund creates a refund for the order via Stripe API.
// If amount is nil, performs full refund. Otherwise refunds the specified amount in the given currency.
// Requires payment with valid ClientSecret (PaymentIntent) and IsTransactionDone.
func (p *Processor) Refund(ctx context.Context, payment entity.Payment, orderUUID string, amount *decimal.Decimal, currency string) error {
	ok := payment.ClientSecret.Valid
	if !ok {
		return fmt.Errorf("payment has no client secret (PaymentIntent)")
	}

	paymentIntentID := trimSecret(payment.ClientSecret.String)
	params := &stripe.RefundParams{
		PaymentIntent: stripe.String(paymentIntentID),
		Reason:        stripe.String("requested_by_customer"),
	}

	// If amount is specified, set it (for partial refunds). Otherwise omit for full refund.
	if amount != nil && !amount.IsZero() {
		rounded := dto.RoundForCurrency(*amount, currency)
		if rounded.IsZero() {
			return fmt.Errorf("refund amount rounds to zero for %s", currency)
		}
		if err := dto.ValidatePriceMeetsMinimum(rounded, currency); err != nil {
			return fmt.Errorf("refund amount below currency minimum: %w", err)
		}
		amountCents := AmountToSmallestUnit(rounded, currency)
		if amountCents <= 0 {
			return fmt.Errorf("refund amount too small for %s", currency)
		}
		params.Amount = stripe.Int64(amountCents)
	}

	params.SetIdempotencyKey(orderUUID)

	_, err := p.stripeClient.Refunds.New(params)
	if err != nil {
		return fmt.Errorf("stripe refund: %w", err)
	}
	return nil
}

// SetReservationManager sets the stock reservation manager for this processor
func (p *Processor) SetReservationManager(mgr dependency.StockReservationManager) {
	p.reservationMgr = mgr
}

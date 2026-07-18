package stripe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4mp"
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

// PaymentIntentSucceeded reports whether a PaymentIntent has already been paid.
// A succeeded PaymentIntent can no longer have its amount/shipping updated, so
// callers should skip those mutations instead of failing.
func PaymentIntentSucceeded(pi *stripe.PaymentIntent) bool {
	return pi != nil && pi.Status == stripe.PaymentIntentStatusSucceeded
}

// AmountToSmallestUnit converts an amount to the smallest currency unit for Stripe.
// The factor is 10^DecimalPlaces — 1 for zero-decimal (JPY, KRW), 100 for
// two-decimal, 1000 for three-decimal — so the conversion is correct for every
// currency exponent instead of a hardcoded x100 that would under-charge a
// three-decimal currency 10x. The amount is rounded to the currency precision
// first so sub-minor-unit noise doesn't leak into the integer.
func AmountToSmallestUnit(amount decimal.Decimal, c string) int64 {
	factor := decimal.New(1, curr.DecimalPlaces(c))
	return curr.Round(amount, c).Mul(factor).IntPart()
}

// AmountFromSmallestUnit converts an amount from Stripe's smallest currency unit
// back to decimal, dividing by 10^DecimalPlaces.
func AmountFromSmallestUnit(amount int64, c string) decimal.Decimal {
	factor := decimal.New(1, curr.DecimalPlaces(c))
	return decimal.NewFromInt(amount).Div(factor)
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

// createPaymentIntent creates a PaymentIntent with the specified amount, currency, and payment method types.
// idempotencyKey must be unique per PaymentIntent; use rotation (e.g. orderUUID+"_"+timestamp) when re-creating after expiry.
func (p *Processor) createPaymentIntent(order entity.OrderFull, idempotencyKey string) (*stripe.PaymentIntent, error) {
	// Stripe boundary: a currency that is priced/accounted but not Stripe-chargeable (USDT) is settled
	// manually off-Stripe and must never be sent to Stripe. Reject it here before any Stripe call.
	if !dto.IsStripeChargeable(order.Order.Currency) {
		return nil, fmt.Errorf("currency %s cannot be charged via Stripe; it is settled manually", order.Order.Currency)
	}
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
	params.SetIdempotencyKey(idempotencyKey)

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
	// Stripe boundary: never push a priced-but-not-Stripe-chargeable currency (USDT) to Stripe. Such an
	// order never gets a PaymentIntent (createPaymentIntent/CreatePreOrderPaymentIntent reject it), so
	// this is defence-in-depth guaranteeing no currency-carrying Stripe call can leak USDT.
	if !dto.IsStripeChargeable(currency) {
		return fmt.Errorf("currency %s cannot be charged via Stripe; it is settled manually", currency)
	}
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

// RefundIdempotencyKey derives a DETERMINISTIC Stripe idempotency key for a refund
// from its scope, so that a retry of the same refund AND two concurrent identical
// refund calls reuse the same key — Stripe then dedupes the operation server-side and
// only moves money once. The key changes when the scope changes (different items,
// quantities, shipping flag, full-vs-partial, or amount), so distinct partial refunds
// of the same order still get distinct keys.
//
// orderItemIDs may contain repeated IDs (each occurrence = 1 unit); empty = full refund.
func RefundIdempotencyKey(orderUUID string, orderItemIDs []int32, refundShipping bool, amount *decimal.Decimal, currency string) string {
	// Canonicalize the item scope: count units per id, then emit in sorted id order so
	// input ordering does not affect the key.
	counts := make(map[int32]int, len(orderItemIDs))
	for _, id := range orderItemIDs {
		counts[id]++
	}
	ids := make([]int32, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	full := len(orderItemIDs) == 0
	var b strings.Builder
	fmt.Fprintf(&b, "scope=%t;shipping=%t;currency=%s;items=", full, refundShipping, currency)
	for _, id := range ids {
		fmt.Fprintf(&b, "%d:%d,", id, counts[id])
	}
	b.WriteString(";amount=")
	if amount != nil {
		b.WriteString(dto.RoundForCurrency(*amount, currency).String())
	} else {
		b.WriteString("full")
	}

	sum := sha256.Sum256([]byte(b.String()))
	return "refund:" + orderUUID + ":" + hex.EncodeToString(sum[:])
}

// Refund creates a refund for the order via Stripe API.
// If amount is nil, performs full refund. Otherwise refunds the specified amount in the given currency.
// Requires payment with valid ClientSecret (PaymentIntent) and IsTransactionDone.
// idempotencyKey MUST be derived deterministically from the refund scope (see
// RefundIdempotencyKey) so that retries and concurrent identical refunds dedupe at
// Stripe instead of issuing a second refund.
func (p *Processor) Refund(ctx context.Context, payment entity.Payment, orderUUID string, amount *decimal.Decimal, currency string, idempotencyKey string) error {
	ok := payment.ClientSecret.Valid
	if !ok {
		return fmt.Errorf("payment has no client secret (PaymentIntent)")
	}
	if idempotencyKey == "" {
		return fmt.Errorf("refund idempotency key is empty")
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

	// Deterministic idempotency key: retries and concurrent identical refunds reuse the
	// same key, so Stripe refunds the money at most once for this scope.
	params.SetIdempotencyKey(idempotencyKey)

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

// SetGA4MP sets the GA4 Measurement Protocol client for server-side purchase tracking
func (p *Processor) SetGA4MP(c *ga4mp.Client) {
	p.ga4mp = c
}

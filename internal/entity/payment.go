package entity

import (
	"database/sql"
	"time"

	"github.com/shopspring/decimal"
)

type PaymentOrderUUID struct {
	OrderUUID string `db:"order_uuid"`
	Payment   Payment
}

// Payment represents the payment table
type Payment struct {
	Id         int       `db:"id"`
	CreatedAt  time.Time `db:"created_at"`
	ModifiedAt time.Time `db:"modified_at"`
	PaymentInsert
}

type PaymentInsert struct {
	OrderId                          int             `db:"order_id"`
	PaymentMethodID                  int             `db:"payment_method_id"`
	TransactionID                    sql.NullString  `db:"transaction_id"`
	TransactionAmount                decimal.Decimal `db:"transaction_amount"`
	TransactionAmountPaymentCurrency decimal.Decimal `db:"transaction_amount_payment_currency"`
	ClientSecret                     sql.NullString  `db:"client_secret"`
	IsTransactionDone                bool            `db:"is_transaction_done"`
	ExpiredAt                        sql.NullTime    `db:"expired_at"`
	// PaymentMethodType is the provider's sub-method used. It carries the most specific
	// label we can derive from the Stripe charge: the card wallet (apple_pay, google_pay,
	// link) when the card was tokenised through a wallet, otherwise the payment-method type
	// (card, klarna, sepa_debit, ...). NULL for pre-feature / non-Stripe / uncaptured.
	PaymentMethodType sql.NullString `db:"payment_method_type"`
	// CardBrand / CardLast4 identify the funding card on the Stripe charge (visa, 4242).
	// Admin-only detail for support and dispute lookups; empty for non-card methods.
	CardBrand sql.NullString `db:"card_brand"`
	CardLast4 sql.NullString `db:"card_last4"`
	// ReceiptURL is Stripe's hosted receipt for the charge. Customer-facing (surfaced on the
	// storefront order too), NULL when the charge produced no receipt (non-Stripe methods).
	ReceiptURL sql.NullString `db:"receipt_url"`
	// StripeExchangeRate is the presentment->settlement FX rate Stripe applied at the sale
	// (from the charge's balance transaction). Pairs with customer_order.total_settled_base to
	// explain a non-base-currency sale. NULL for base-currency or uncaptured payments.
	StripeExchangeRate decimal.NullDecimal `db:"stripe_exchange_rate"`
	// StripeRiskLevel is Stripe Radar's outcome for the charge (normal/elevated/highest),
	// captured for manual fraud review. NULL for non-Stripe / uncaptured payments.
	StripeRiskLevel sql.NullString `db:"stripe_risk_level"`
}

// StripePaymentDetails carries the payment-row facts captured from a succeeded Stripe charge
// (and its balance transaction) at confirmation time. It is written best-effort after the
// order is marked paid; see Processor.capturePaymentDetails.
type StripePaymentDetails struct {
	// TransactionID is the Stripe PaymentIntent id (pi_...), stored in payment.transaction_id
	// and combined with the payment method to build the dashboard deep link.
	TransactionID      string
	PaymentMethodType  string
	CardBrand          string
	CardLast4          string
	ReceiptURL         string
	StripeExchangeRate decimal.NullDecimal
	StripeRiskLevel    string
}

type PaymentMethodName string

const (
	CARD         PaymentMethodName = "card"
	CARD_TEST    PaymentMethodName = "card-test"
	BANK_INVOICE PaymentMethodName = "bank-invoice"
	CASH         PaymentMethodName = "cash"
)

// ValidPaymentMethodNames is a set of valid payment method names
var ValidPaymentMethodNames = map[PaymentMethodName]bool{
	CARD:         true,
	CARD_TEST:    true,
	BANK_INVOICE: true,
	CASH:         true,
}

// PaymentMethod represents the payment_method table
type PaymentMethod struct {
	Id      int               `db:"id"`
	Name    PaymentMethodName `db:"name"`
	Allowed bool              `db:"allowed"`
	// FeePct + FeeFixed model the processing fee of this method, used to ESTIMATE the fee of an
	// order that has no captured Stripe fee (bank-invoice / cash / non-EUR-settled / legacy).
	FeePct   decimal.Decimal `db:"fee_pct"`
	FeeFixed decimal.Decimal `db:"fee_fixed"`
}

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
	ID         int       `db:"id"`
	CreatedAt  time.Time `db:"created_at"`
	ModifiedAt time.Time `db:"modified_at"`
	PaymentInsert
}

type PaymentInsert struct {
	PaymentMethodID                  int             `db:"payment_method_id"`
	TransactionID                    sql.NullString  `db:"transaction_id"`
	TransactionAmount                decimal.Decimal `db:"transaction_amount"`
	TransactionAmountPaymentCurrency decimal.Decimal `db:"transaction_amount_payment_currency"`
	Payer                            sql.NullString  `db:"payer"`
	Payee                            sql.NullString  `db:"payee"`
	IsTransactionDone                bool            `db:"is_transaction_done"`
}

type PaymentMethodName string

const (
	CARD           PaymentMethodName = "card"
	ETH            PaymentMethodName = "eth"
	USDT_TRON      PaymentMethodName = "usdt-tron"
	USDT_TRON_TEST PaymentMethodName = "usdt-shasta"
)

// ValidPaymentMethodNames is a set of valid payment method names
var ValidPaymentMethodNames = map[PaymentMethodName]bool{
	CARD:           true,
	ETH:            true,
	USDT_TRON:      true,
	USDT_TRON_TEST: true,
}

// PaymentMethod represents the payment_method table
type PaymentMethod struct {
	ID      int               `db:"id"`
	Name    PaymentMethodName `db:"name"`
	Allowed bool              `db:"allowed"`
}

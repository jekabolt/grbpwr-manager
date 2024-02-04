package entity

import (
	"database/sql"
	"time"

	"github.com/shopspring/decimal"
)

// Payment represents the payment table
type Payment struct {
	ID         int       `db:"id"`
	CreatedAt  time.Time `db:"created_at"`
	ModifiedAt time.Time `db:"modified_at"`
	PaymentInsert
}

type PaymentInsert struct {
	PaymentMethodID   int             `db:"payment_method_id"`
	TransactionID     sql.NullString  `db:"transaction_id"`
	TransactionAmount decimal.Decimal `db:"transaction_amount"`
	Payer             sql.NullString  `db:"payer"`
	Payee             sql.NullString  `db:"payee"`
	IsTransactionDone bool            `db:"is_transaction_done"`
}

type PaymentMethodName string

const (
	Card PaymentMethodName = "card"
	Eth  PaymentMethodName = "eth"
	Usdc PaymentMethodName = "usdc"
	Usdt PaymentMethodName = "usdt"
)

// ValidPaymentMethodNames is a set of valid payment method names
var ValidPaymentMethodNames = map[PaymentMethodName]bool{
	Card: true,
	Eth:  true,
	Usdc: true,
	Usdt: true,
}

// PaymentMethod represents the payment_method table
type PaymentMethod struct {
	ID      int               `db:"id"`
	Name    PaymentMethodName `db:"name"`
	Allowed bool              `db:"allowed"`
}

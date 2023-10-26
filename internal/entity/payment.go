package entity

import (
	"time"

	"github.com/shopspring/decimal"
)

// Payment represents the payment table
type Payment struct {
	ID                int             `db:"id"`
	PaymentMethodID   int             `db:"payment_method_id"`
	TransactionID     string          `db:"transaction_id"`
	TransactionAmount decimal.Decimal `db:"transaction_amount"`
	Payer             string          `db:"payer"`
	Payee             string          `db:"payee"`
	IsTransactionDone bool            `db:"is_transaction_done"`
	CreatedAt         time.Time       `db:"created_at"`
	ModifiedAt        time.Time       `db:"modified_at"`
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
	ID   int               `db:"id"`
	Name PaymentMethodName `db:"name"`
}

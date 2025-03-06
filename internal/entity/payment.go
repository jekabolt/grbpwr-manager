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
	Payer                            sql.NullString  `db:"payer"`
	Payee                            sql.NullString  `db:"payee"`
	ClientSecret                     sql.NullString  `db:"client_secret"`
	IsTransactionDone                bool            `db:"is_transaction_done"`
	ExpiredAt                        sql.NullTime    `db:"expired_at"`
}

type PaymentMethodName string

const (
	CARD           PaymentMethodName = "card"
	CARD_TEST      PaymentMethodName = "card-test"
	ETH            PaymentMethodName = "eth"
	ETH_TEST       PaymentMethodName = "eth-test"
	USDT_TRON      PaymentMethodName = "usdt-tron"
	USDT_TRON_TEST PaymentMethodName = "usdt-shasta"
)

// ValidPaymentMethodNames is a set of valid payment method names
var ValidPaymentMethodNames = map[PaymentMethodName]bool{
	CARD:           true,
	CARD_TEST:      true,
	ETH:            true,
	ETH_TEST:       true,
	USDT_TRON:      true,
	USDT_TRON_TEST: true,
}

// PaymentMethod represents the payment_method table
type PaymentMethod struct {
	Id      int               `db:"id"`
	Name    PaymentMethodName `db:"name"`
	Allowed bool              `db:"allowed"`
}

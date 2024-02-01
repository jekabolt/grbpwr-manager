package entity

import (
	"time"

	"github.com/shopspring/decimal"
)

type CurrencyRate struct {
	Id           int             `db:"id"`
	CurrencyCode string          `db:"currency_code"`
	Rate         decimal.Decimal `db:"rate"`
	UpdatedAt    time.Time       `db:"updated_at"`
}

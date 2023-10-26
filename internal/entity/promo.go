package entity

import (
	"github.com/shopspring/decimal"
)

// PromoCode represents the promo_code table
type PromoCode struct {
	ID int `db:"id"`
	PromoCodeInsert
}

type PromoCodeInsert struct {
	Code         string          `db:"code"`
	FreeShipping bool            `db:"free_shipping"`
	Discount     decimal.Decimal `db:"discount"`
	Expiration   int64           `db:"expiration"`
	Allowed      bool            `db:"allowed"`
}

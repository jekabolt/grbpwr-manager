package entity

import (
	"time"

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
	Expiration   time.Time       `db:"expiration"`
	Voucher      bool            `db:"voucher"`
	Allowed      bool            `db:"allowed"`
}

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

func (pc *PromoCode) IsAllowed() bool {
	return pc.Allowed && pc.Expiration.After(time.Now())
}

func (pc *PromoCode) SubtotalWithPromo(subtotal, shippingPrice decimal.Decimal) decimal.Decimal {
	if !pc.Discount.Equals(decimal.Zero) {
		subtotal = subtotal.Mul(decimal.NewFromInt(100).Sub(pc.Discount).Div(decimal.NewFromInt(100))).Round(2)
	}

	if !pc.FreeShipping {
		subtotal = subtotal.Add(shippingPrice).Round(2)
	}
	return subtotal
}

type PromoCodeInsert struct {
	Code         string          `db:"code"`
	FreeShipping bool            `db:"free_shipping"`
	Discount     decimal.Decimal `db:"discount"`
	Expiration   time.Time       `db:"expiration"`
	Voucher      bool            `db:"voucher"`
	Allowed      bool            `db:"allowed"`
}

func (pc *PromoCode) DiscountDecimal() decimal.Decimal {
	return pc.Discount.Round(2)
}

package entity

import (
	"time"

	"github.com/shopspring/decimal"
)

// PromoCode represents the promo_code table
type PromoCode struct {
	Id int `db:"id"`
	PromoCodeInsert
}

func (pc *PromoCode) IsAllowed() bool {
	return pc.Allowed && pc.Expiration.After(time.Now()) && pc.Start.Before(time.Now())
}

// SubtotalWithPromo applies promo discount and shipping. decimalPlaces: 0 for zero-decimal (KRW, JPY), 2 for standard.
func (pc *PromoCode) SubtotalWithPromo(subtotal, shippingPrice decimal.Decimal, decimalPlaces int32) decimal.Decimal {
	if !pc.Discount.Equals(decimal.Zero) {
		subtotal = subtotal.Mul(decimal.NewFromInt(100).Sub(pc.Discount).Div(decimal.NewFromInt(100))).Round(decimalPlaces)
	}

	if !pc.FreeShipping {
		subtotal = subtotal.Add(shippingPrice).Round(decimalPlaces)
	}
	return subtotal
}

type PromoCodeInsert struct {
	Code         string          `db:"code"`
	FreeShipping bool            `db:"free_shipping"`
	Discount     decimal.Decimal `db:"discount"`
	Expiration   time.Time       `db:"expiration"`
	Start        time.Time       `db:"start"`
	Voucher      bool            `db:"voucher"`
	Allowed      bool            `db:"allowed"`
}

func (pc *PromoCode) DiscountDecimal() decimal.Decimal {
	return pc.Discount.Round(2)
}

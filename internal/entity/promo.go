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
	return pc.Allowed && pc.Expiration.After(time.Now().UTC()) && pc.Start.Before(time.Now().UTC())
}

// CalculateTotalWithPromo applies promo discount and shipping to calculate final total.
// Discount is applied to subtotal only, then shipping is added (unless free shipping applies).
// Returns the final total and whether free shipping was granted.
// decimalPlaces: 0 for zero-decimal currencies (KRW, JPY), 2 for standard.
func (pc *PromoCode) CalculateTotalWithPromo(subtotal, shippingPrice decimal.Decimal, decimalPlaces int32) (total decimal.Decimal, freeShippingGranted bool) {
	// Validate promo is currently active
	if !pc.IsAllowed() {
		// Promo not valid — return subtotal + shipping with no discount
		return subtotal.Add(shippingPrice).Round(decimalPlaces), false
	}

	// Apply percentage discount to subtotal only
	discountedSubtotal := subtotal
	if !pc.Discount.Equals(decimal.Zero) {
		discountedSubtotal = subtotal.Mul(decimal.NewFromInt(100).Sub(pc.Discount).Div(decimal.NewFromInt(100))).Round(decimalPlaces)
	}

	// Add shipping (or waive if promo grants free shipping)
	if pc.FreeShipping {
		return discountedSubtotal.Round(decimalPlaces), true
	}

	return discountedSubtotal.Add(shippingPrice).Round(decimalPlaces), false
}

// SubtotalWithPromo applies promo discount and shipping. decimalPlaces: 0 for zero-decimal (KRW, JPY), 2 for standard.
// Deprecated: Use CalculateTotalWithPromo instead, which validates time windows and returns free shipping status.
func (pc *PromoCode) SubtotalWithPromo(subtotal, shippingPrice decimal.Decimal, decimalPlaces int32) decimal.Decimal {
	total, _ := pc.CalculateTotalWithPromo(subtotal, shippingPrice, decimalPlaces)
	return total
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

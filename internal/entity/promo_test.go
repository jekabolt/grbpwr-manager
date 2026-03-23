package entity

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestPromoCode_CalculateTotalWithPromo(t *testing.T) {
	now := time.Now().UTC()
	validPromo := PromoCode{
		Id: 1,
		PromoCodeInsert: PromoCodeInsert{
			Code:         "SAVE10",
			Discount:     decimal.NewFromInt(10),
			FreeShipping: false,
			Allowed:      true,
			Start:        now.Add(-24 * time.Hour),
			Expiration:   now.Add(24 * time.Hour),
		},
	}

	expiredPromo := PromoCode{
		Id: 2,
		PromoCodeInsert: PromoCodeInsert{
			Code:         "EXPIRED",
			Discount:     decimal.NewFromInt(20),
			FreeShipping: true,
			Allowed:      true,
			Start:        now.Add(-48 * time.Hour),
			Expiration:   now.Add(-24 * time.Hour),
		},
	}

	freeShippingPromo := PromoCode{
		Id: 3,
		PromoCodeInsert: PromoCodeInsert{
			Code:         "FREESHIP",
			Discount:     decimal.NewFromInt(0),
			FreeShipping: true,
			Allowed:      true,
			Start:        now.Add(-24 * time.Hour),
			Expiration:   now.Add(24 * time.Hour),
		},
	}

	comboPromo := PromoCode{
		Id: 4,
		PromoCodeInsert: PromoCodeInsert{
			Code:         "COMBO15",
			Discount:     decimal.NewFromInt(15),
			FreeShipping: true,
			Allowed:      true,
			Start:        now.Add(-24 * time.Hour),
			Expiration:   now.Add(24 * time.Hour),
		},
	}

	tests := []struct {
		name                     string
		promo                    PromoCode
		subtotal                 decimal.Decimal
		shippingPrice            decimal.Decimal
		decimalPlaces            int32
		expectedTotal            string
		expectedFreeShipping     bool
		description              string
	}{
		{
			name:                 "10% discount on subtotal, shipping added",
			promo:                validPromo,
			subtotal:             decimal.NewFromInt(100),
			shippingPrice:        decimal.NewFromInt(15),
			decimalPlaces:        2,
			expectedTotal:        "105",
			expectedFreeShipping: false,
			description:          "Discount applies to subtotal only: 100 * 0.9 = 90, then add 15 shipping = 105",
		},
		{
			name:                 "expired promo should not apply",
			promo:                expiredPromo,
			subtotal:             decimal.NewFromInt(100),
			shippingPrice:        decimal.NewFromInt(15),
			decimalPlaces:        2,
			expectedTotal:        "115",
			expectedFreeShipping: false,
			description:          "Expired promo ignored: 100 + 15 = 115 (no discount or free shipping)",
		},
		{
			name:                 "free shipping promo, no discount",
			promo:                freeShippingPromo,
			subtotal:             decimal.NewFromInt(100),
			shippingPrice:        decimal.NewFromInt(15),
			decimalPlaces:        2,
			expectedTotal:        "100",
			expectedFreeShipping: true,
			description:          "Free shipping waives the 15 shipping cost",
		},
		{
			name:                 "combo: 15% discount + free shipping",
			promo:                comboPromo,
			subtotal:             decimal.NewFromInt(100),
			shippingPrice:        decimal.NewFromInt(15),
			decimalPlaces:        2,
			expectedTotal:        "85",
			expectedFreeShipping: true,
			description:          "15% off subtotal: 100 * 0.85 = 85, free shipping waives 15",
		},
		{
			name:                 "zero decimal currency (KRW)",
			promo:                validPromo,
			subtotal:             decimal.NewFromInt(10000),
			shippingPrice:        decimal.NewFromInt(3000),
			decimalPlaces:        0,
			expectedTotal:        "12000",
			expectedFreeShipping: false,
			description:          "10% off 10000 = 9000, plus 3000 shipping = 12000 (no decimal places)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, freeShipping := tt.promo.CalculateTotalWithPromo(tt.subtotal, tt.shippingPrice, tt.decimalPlaces)

			if total.String() != tt.expectedTotal {
				t.Errorf("CalculateTotalWithPromo() total = %v, want %v\nContext: %s", total.String(), tt.expectedTotal, tt.description)
			}

			if freeShipping != tt.expectedFreeShipping {
				t.Errorf("CalculateTotalWithPromo() freeShipping = %v, want %v\nContext: %s", freeShipping, tt.expectedFreeShipping, tt.description)
			}
		})
	}
}

func TestPromoCode_IsAllowed(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name     string
		promo    PromoCode
		expected bool
	}{
		{
			name: "valid promo within time window",
			promo: PromoCode{
				PromoCodeInsert: PromoCodeInsert{
					Allowed:    true,
					Start:      now.Add(-24 * time.Hour),
					Expiration: now.Add(24 * time.Hour),
				},
			},
			expected: true,
		},
		{
			name: "expired promo",
			promo: PromoCode{
				PromoCodeInsert: PromoCodeInsert{
					Allowed:    true,
					Start:      now.Add(-48 * time.Hour),
					Expiration: now.Add(-24 * time.Hour),
				},
			},
			expected: false,
		},
		{
			name: "not yet started",
			promo: PromoCode{
				PromoCodeInsert: PromoCodeInsert{
					Allowed:    true,
					Start:      now.Add(24 * time.Hour),
					Expiration: now.Add(48 * time.Hour),
				},
			},
			expected: false,
		},
		{
			name: "disabled promo (Allowed=false)",
			promo: PromoCode{
				PromoCodeInsert: PromoCodeInsert{
					Allowed:    false,
					Start:      now.Add(-24 * time.Hour),
					Expiration: now.Add(24 * time.Hour),
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.promo.IsAllowed()
			if result != tt.expected {
				t.Errorf("IsAllowed() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestPromoCode_SubtotalWithPromo_BackwardCompatibility(t *testing.T) {
	now := time.Now().UTC()
	promo := PromoCode{
		Id: 1,
		PromoCodeInsert: PromoCodeInsert{
			Code:         "SAVE10",
			Discount:     decimal.NewFromInt(10),
			FreeShipping: false,
			Allowed:      true,
			Start:        now.Add(-24 * time.Hour),
			Expiration:   now.Add(24 * time.Hour),
		},
	}

	subtotal := decimal.NewFromInt(100)
	shipping := decimal.NewFromInt(15)

	legacyResult := promo.SubtotalWithPromo(subtotal, shipping, 2)
	newResult, _ := promo.CalculateTotalWithPromo(subtotal, shipping, 2)

	if !legacyResult.Equal(newResult) {
		t.Errorf("SubtotalWithPromo() backward compatibility broken: legacy=%v, new=%v", legacyResult, newResult)
	}
}

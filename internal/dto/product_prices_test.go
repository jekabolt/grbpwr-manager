package dto

import (
	"testing"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

func TestConvertCommonProductToEntityResolvesPriceOwner(t *testing.T) {
	tests := []struct {
		name     string
		topLevel []*pb_common.ColorwayPriceInsert
		legacy   []*pb_common.ColorwayPriceInsert
		want     string
		wantErr  string
	}{
		{
			name:     "canonical top-level prices",
			topLevel: []*pb_common.ColorwayPriceInsert{priceInsert("eur", "120.001")},
			want:     "120",
		},
		{
			name:   "legacy nested prices remain compatible",
			legacy: []*pb_common.ColorwayPriceInsert{priceInsert("eur", "120.001")},
			want:   "120",
		},
		{
			name:     "identical duplicate prices remain compatible",
			topLevel: []*pb_common.ColorwayPriceInsert{priceInsert("EUR", "120")},
			legacy:   []*pb_common.ColorwayPriceInsert{priceInsert("eur", "120.00")},
			want:     "120",
		},
		{
			name:     "conflicting duplicate prices are rejected",
			topLevel: []*pb_common.ColorwayPriceInsert{priceInsert("EUR", "120")},
			legacy:   []*pb_common.ColorwayPriceInsert{priceInsert("EUR", "125")},
			wantErr:  "conflicting prices",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConvertCommonProductToEntity(colorwayWithPrices(tt.topLevel, tt.legacy))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.Len(t, got.Prices, 1)
			require.Len(t, got.Product.Prices, 1)
			require.Equal(t, "EUR", got.Prices[0].Currency)
			require.True(t, got.Prices[0].Price.Equal(decimal.RequireFromString(tt.want)))
			require.Equal(t, got.Prices, got.Product.Prices)
		})
	}
}

func colorwayWithPrices(topLevel, legacy []*pb_common.ColorwayPriceInsert) *pb_common.ColorwayNew {
	return &pb_common.ColorwayNew{
		Product: &pb_common.ColorwayInsert{
			ProductBodyInsert: &pb_common.ColorwayBodyInsert{
				TargetGender: pb_common.GenderEnum_GENDER_ENUM_UNISEX,
				Season:       pb_common.SeasonEnum_SEASON_ENUM_SS,
				ColorCode:    "BLK",
			},
			Prices: legacy,
		},
		Prices: topLevel,
	}
}

func priceInsert(currency, value string) *pb_common.ColorwayPriceInsert {
	return &pb_common.ColorwayPriceInsert{
		Currency: currency,
		Price:    &pb_decimal.Decimal{Value: value},
	}
}

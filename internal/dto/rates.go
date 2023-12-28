package dto

import (
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

type CurrencyRate struct {
	Description string
	Rate        decimal.Decimal
}

func CurrencyRateToPb(cm map[string]CurrencyRate) *pb_common.CurrencyMap {
	pbCm := pb_common.CurrencyMap{
		Currencies: make(map[string]*pb_common.CurrencyRate, len(cm)),
	}
	for k, v := range cm {
		pbCm.Currencies[k] = &pb_common.CurrencyRate{
			Description: v.Description,
			Rate: &pb_decimal.Decimal{
				Value: v.Rate.String(),
			},
		}
	}
	return &pbCm
}

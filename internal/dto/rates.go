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

func CurrencyRateToPb(cm map[CurrencyTicker]CurrencyRate) *pb_common.CurrencyMap {
	pbCm := pb_common.CurrencyMap{
		Currencies: make(map[string]*pb_common.CurrencyRate, len(cm)),
	}
	for k, v := range cm {
		pbCm.Currencies[k.String()] = &pb_common.CurrencyRate{
			Description: v.Description,
			Rate: &pb_decimal.Decimal{
				Value: v.Rate.String(),
			},
		}
	}
	return &pbCm
}

type CurrencyTicker string

func (ct CurrencyTicker) String() string { return string(ct) }

const (
	BTC CurrencyTicker = "BTC"
	ETH CurrencyTicker = "ETH"
	CHF CurrencyTicker = "CHF"
	CNY CurrencyTicker = "CNY"
	CZK CurrencyTicker = "CZK"
	DKK CurrencyTicker = "DKK"
	EUR CurrencyTicker = "EUR"
	GBP CurrencyTicker = "GBP"
	GEL CurrencyTicker = "GEL"
	HKD CurrencyTicker = "HKD"
	HUF CurrencyTicker = "HUF"
	ILS CurrencyTicker = "ILS"
	JPY CurrencyTicker = "JPY"
	NOK CurrencyTicker = "NOK"
	PLN CurrencyTicker = "PLN"
	RUB CurrencyTicker = "RUB"
	SEK CurrencyTicker = "SEK"
	SGD CurrencyTicker = "SGD"
	TRY CurrencyTicker = "TRY"
	UAH CurrencyTicker = "UAH"
	USD CurrencyTicker = "USD"
)

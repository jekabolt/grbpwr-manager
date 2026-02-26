package entity

import "github.com/shopspring/decimal"

type DictionaryInfo struct {
	Categories                  []Category
	Measurements                []MeasurementName
	PaymentMethods              []PaymentMethod
	OrderStatuses               []OrderStatus
	Promos                      []PromoCode
	ShipmentCarriers            []ShipmentCarrier
	Sizes                       []Size
	Collections                 []Collection
	Languages                   []Language
	Announce                    *AnnounceWithTranslations
	ComplimentaryShippingPrices map[string]decimal.Decimal
}

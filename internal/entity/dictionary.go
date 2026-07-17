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
	BackgroundHeroColor         string
	ProductTags                 []string
	Colors                      []Color
	// CategorySizeSystems is the category -> permitted size-system(s) mapping (S10/WS5, migration
	// 0175), used both by the admin size picker (dictionary output) and, resolved against a style's
	// category path, by server-side size-write validation (ResolveSizeSystemPolicy).
	CategorySizeSystems []CategorySizeSystem
	// Fibers is the controlled fibre vocabulary (S17/P0.4) surfaced to the admin so a composition
	// editor can pick a fibre by code; archived entries are included, flagged via ArchivedAt.
	Fibers []Fiber
}

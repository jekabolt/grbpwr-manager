package entity

type DictionaryInfo struct {
	Categories       []Category
	Measurements     []MeasurementName
	PaymentMethods   []PaymentMethod
	OrderStatuses    []OrderStatus
	Promos           []PromoCode
	ShipmentCarriers []ShipmentCarrier
	Sizes            []Size
	Languages        []Language
}

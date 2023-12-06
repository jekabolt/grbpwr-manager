package dto

type OrderConfirmed struct {
	Name            string
	OrderID         string
	OrderDate       string
	TotalAmount     float64
	PaymentMethod   string
	PaymentCurrency string
}

type OrderCancelled struct {
	Name             string
	OrderID          string
	CancellationDate string
	RefundAmount     float64
	PaymentMethod    string
	PaymentCurrency  string
}

type OrderShipment struct {
	Name           string
	OrderID        string
	ShippingDate   string
	TotalAmount    float64
	TrackingNumber string
	TrackingURL    string
}
type PromoCodeDetails struct {
	PromoCode       string
	HasFreeShipping bool
	DiscountAmount  int
	ExpirationDate  string
}

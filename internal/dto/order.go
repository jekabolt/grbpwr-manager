// Package dto contains data transfer objects for orders.
package dto

import (
	"time"

	"github.com/shopspring/decimal"
)

// PaymentMethod represents the method of payment for an order.
type PaymentMethod string

const (
	// CardPayment indicates payment with a credit or debit card.
	CardPayment PaymentMethod = "Card"
	// Ethereum indicates payment with Ethereum cryptocurrency.
	Ethereum PaymentMethod = "ETH"
	// USDC indicates payment with USD Coin cryptocurrency.
	USDC PaymentMethod = "USDC"
	// USDT indicates payment with Tether cryptocurrency.
	USDT PaymentMethod = "USDT"
)

// PaymentCurrency represents the currency of payment for an order.
type PaymentCurrency string

const (
	// EUR indicates payment with a credit or debit card in euro.
	EUR PaymentCurrency = "EUR"
	// USD indicates payment with a credit or debit card in us dollar.
	USD PaymentCurrency = "USD"
	// USDCrypto indicates payment with USD Coin cryptocurrency.
	USDCrypto PaymentCurrency = "USDC"
	// USDCrypto indicates payment with Ethereum cryptocurrency.
	ETH PaymentCurrency = "ETH"
)

// OrderStatus represents the current status of an order.
type OrderStatus string

const (
	// OrderPlaced indicates that the order has been placed.
	OrderPlaced OrderStatus = "Placed"
	// OrderConfirmed indicates that the order has been confirmed.
	OrderConfirmed OrderStatus = "Confirmed"
	// OrderShipped indicates that the order has been shipped.
	OrderShipped OrderStatus = "Shipped"
	// OrderDelivered indicates that the order has been delivered.
	OrderDelivered OrderStatus = "Delivered"
	// OrderCancelled indicates that the order has been cancelled.
	OrderCancelled OrderStatus = "Cancelled"
	// OrderRefunded indicates that the order has been refunded.
	OrderRefunded OrderStatus = "Refunded"
)

// Payment represents the payment details for an order.
type Payment struct {
	ID                int64
	Method            PaymentMethod
	Currency          PaymentCurrency
	TransactionID     string
	TransactionAmount decimal.Decimal
	Payer             string
	Payee             string
	IsTransactionDone bool
}

// Address represents a physical address.
type Address struct {
	ID              int64
	Street          string
	HouseNumber     string
	ApartmentNumber string
	City            string
	State           string
	Country         string
	PostalCode      string
}

// Buyer represents a person or entity purchasing items.
type Buyer struct {
	ID                 int64
	FirstName          string
	LastName           string
	Email              string
	Phone              string
	BillingAddress     *Address
	ShippingAddress    *Address
	ReceivePromoEmails bool
}

// Shipment represents the shipment details for an order.
type Shipment struct {
	ID                   int64
	Carrier              string
	Cost                 decimal.Decimal
	TrackingCode         string
	ShippingDate         time.Time
	EstimatedArrivalDate time.Time
}

// Order represents a purchase order.
type Order struct {
	ID         int64
	Buyer      *Buyer
	Placed     time.Time
	Items      []Item
	Payment    *Payment
	Shipment   *Shipment
	TotalPrice decimal.Decimal
	PromoCode  *PromoCode
	Status     OrderStatus
}

type Item struct {
	// ID is the ID of the product.
	ID int64
	// Quantity is the number of items of this product in the order in corresponding size.
	Quantity int
	// Size is the size of the product in the order.
	Size string
}

package entity

import (
	"time"

	"github.com/shopspring/decimal"
)

type OrderInfo struct {
	Order           *Order
	OrderItems      []OrderItem
	Payment         *Payment
	PaymentMethod   *PaymentMethod
	Shipment        *Shipment
	ShipmentCarrier *ShipmentCarrier
	PromoCode       *PromoCode
	OrderStatus     *OrderStatus
	Buyer           *Buyer
	Billing         *Address
	Shipping        *Address
	Placed          time.Time
	Modified        time.Time
	TotalPrice      decimal.Decimal
}

// Orders represents the orders table
type Order struct {
	ID            int             `db:"id"`
	BuyerID       int             `db:"buyer_id"`
	Placed        time.Time       `db:"placed"`
	Modified      time.Time       `db:"modified"`
	PaymentID     int             `db:"payment_id"`
	TotalPrice    decimal.Decimal `db:"total_price"`
	OrderStatusID int             `db:"order_status_id"`
	ShipmentId    int             `db:"shipment_id"`
	PromoID       int             `db:"promo_id"`
}

// OrderItem represents the order_item table
type OrderItem struct {
	ID        int `db:"id"`
	OrderID   int `db:"order_id"`
	ProductID int `db:"product_id"`
	Quantity  int `db:"quantity"`
	SizeID    int `db:"size_id"`
}

// OrderStatusName is the custom type to enforce enum-like behavior
type OrderStatusName string

const (
	Placed    OrderStatusName = "placed"
	Confirmed OrderStatusName = "confirmed"
	Shipped   OrderStatusName = "shipped"
	Delivered OrderStatusName = "delivered"
	Cancelled OrderStatusName = "cancelled"
	Refunded  OrderStatusName = "refunded"
)

// ValidOrderStatusNames is a set of valid order status names
var ValidOrderStatusNames = map[OrderStatusName]bool{
	Placed:    true,
	Confirmed: true,
	Shipped:   true,
	Delivered: true,
	Cancelled: true,
	Refunded:  true,
}

// OrderStatus represents the order_status table
type OrderStatus struct {
	ID   int             `db:"id"`
	Name OrderStatusName `db:"name"`
}
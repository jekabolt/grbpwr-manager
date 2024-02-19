package entity

import (
	"database/sql"
	"time"

	"github.com/shopspring/decimal"
)

type OrderNew struct {
	Items             []OrderItemInsert `valid:"required"`
	ShippingAddress   *AddressInsert    `valid:"required"`
	BillingAddress    *AddressInsert    `valid:"required"`
	Buyer             *BuyerInsert      `valid:"required"`
	PaymentMethodId   int               `valid:"required"`
	ShipmentCarrierId int               `valid:"required"`
	PromoCode         string            `valid:"-"`
}

type OrderFull struct {
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
	UUID          string          `db:"uuid"`
	BuyerID       int             `db:"buyer_id"`
	Placed        time.Time       `db:"placed"`
	Modified      time.Time       `db:"modified"`
	PaymentID     int             `db:"payment_id"`
	TotalPrice    decimal.Decimal `db:"total_price"`
	OrderStatusID int             `db:"order_status_id"`
	ShipmentId    int             `db:"shipment_id"`
	PromoID       sql.NullInt32   `db:"promo_id"`
}

type ProductInfoProvider interface {
	GetProductID() int
	GetQuantity() decimal.Decimal
}

// OrderItem represents the order_item table
type OrderItem struct {
	ID        int    `db:"id"`
	OrderID   int    `db:"order_id"`
	Thumbnail string `db:"thumbnail"`
	OrderItemInsert
}

type OrderItemInsert struct {
	ProductID int             `db:"product_id" valid:"required"`
	Quantity  decimal.Decimal `db:"quantity" valid:"required"`
	SizeID    int             `db:"size_id" valid:"required"`
}

func (oii OrderItemInsert) GetProductID() int {
	return oii.ProductID
}

func (oii OrderItemInsert) GetQuantity() decimal.Decimal {
	return oii.Quantity
}

// OrderStatusName is the custom type to enforce enum-like behavior
type OrderStatusName string

func (osn *OrderStatusName) String() string {
	return string(*osn)
}

const (
	Placed          OrderStatusName = "placed"
	AwaitingPayment OrderStatusName = "awaiting_payment"
	Confirmed       OrderStatusName = "confirmed"
	Shipped         OrderStatusName = "shipped"
	Delivered       OrderStatusName = "delivered"
	Cancelled       OrderStatusName = "cancelled"
	Refunded        OrderStatusName = "refunded"
)

// ValidOrderStatusNames is a set of valid order status names
var ValidOrderStatusNames = map[OrderStatusName]bool{
	Placed:          true,
	AwaitingPayment: true,
	Confirmed:       true,
	Shipped:         true,
	Delivered:       true,
	Cancelled:       true,
	Refunded:        true,
}

// OrderStatus represents the order_status table
type OrderStatus struct {
	ID   int             `db:"id"`
	Name OrderStatusName `db:"name"`
}

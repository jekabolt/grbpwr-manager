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
	Order      *Order
	OrderItems []OrderItem
	Payment    *Payment
	Shipment   *Shipment
	PromoCode  *PromoCode
	Buyer      *Buyer
	Billing    *Address
	Shipping   *Address
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
	GetProductPrice() decimal.Decimal
	GetProductSalePercentage() decimal.Decimal
	GetQuantity() decimal.Decimal
	GetSizeId() int
}

// OrderItem represents the order_item table
type OrderItem struct {
	ID           int    `db:"id"`
	OrderID      int    `db:"order_id"`
	Thumbnail    string `db:"thumbnail"`
	ProductName  string `db:"product_name"`
	ProductBrand string `db:"product_brand"`
	CategoryID   int    `db:"category_id"`
	SKU          string `db:"product_sku"`
	OrderItemInsert
}

type OrderItemInsert struct {
	ProductID             int             `db:"product_id" valid:"required"`
	ProductPrice          decimal.Decimal `db:"product_price"`
	ProductSalePercentage decimal.Decimal `db:"product_sale_percentage"`
	Quantity              decimal.Decimal `db:"quantity" valid:"required"`
	SizeID                int             `db:"size_id" valid:"required"`
}

// ByProductID implements sort.Interface for []OrderItemInsert based on the ProductID field.
type OrderItemsByProductID []OrderItemInsert

func (a OrderItemsByProductID) Len() int { return len(a) }
func (a OrderItemsByProductID) Less(i, j int) bool {
	if a[i].ProductID == a[j].ProductID {
		return a[i].SizeID < a[j].SizeID
	}
	return a[i].ProductID < a[j].ProductID
}
func (a OrderItemsByProductID) Swap(i, j int) { a[i], a[j] = a[j], a[i] }

func ConvertOrderItemInsertsToProductInfoProviders(items []OrderItemInsert) []ProductInfoProvider {
	providers := make([]ProductInfoProvider, len(items))
	for i, item := range items {
		providers[i] = &item
	}
	return providers
}

func ConvertOrderItemToOrderItemInsert(items []OrderItem) []OrderItemInsert {
	inserts := make([]OrderItemInsert, len(items))
	for i, item := range items {
		inserts[i] = item.OrderItemInsert
	}
	return inserts
}

func (oii *OrderItemInsert) GetProductID() int {
	return oii.ProductID
}

func (oii *OrderItemInsert) GetProductPrice() decimal.Decimal {
	return oii.ProductPrice
}

func (oii *OrderItemInsert) GetProductSalePercentage() decimal.Decimal {
	return oii.ProductSalePercentage
}

func (oii *OrderItemInsert) GetQuantity() decimal.Decimal {
	return oii.Quantity
}
func (oii *OrderItemInsert) GetSizeId() int {
	return oii.SizeID
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

type OrderBuyerShipment struct {
	Order    *Order
	Buyer    *Buyer
	Shipment *Shipment
}

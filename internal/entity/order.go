package entity

import (
	"database/sql"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/shopspring/decimal"
)

// ValidationError represents a validation error that should return a 4xx status code
// This error type is used to distinguish validation errors from internal server errors
// The API layer should convert this to appropriate HTTP/gRPC status codes
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

type OrderNew struct {
	Items              []OrderItemInsert `valid:"required"`
	ShippingAddress    *AddressInsert    `valid:"required"`
	BillingAddress     *AddressInsert    `valid:"required"`
	Buyer              *BuyerInsert      `valid:"required"`
	PaymentMethod      PaymentMethodName `valid:"required"`
	ShipmentCarrierId  int               `valid:"required"`
	PromoCode          string            `valid:"-"`
	Currency           string            `valid:"required,length(3|3)"` // ISO 4217 currency code
	CustomShipmentCost *decimal.Decimal  `valid:"-"`                    // optional; when set, overrides carrier price (admin custom orders)
}

type OrderFull struct {
	Order              Order
	OrderItems         []OrderItem
	RefundedOrderItems []OrderItem
	Payment            Payment
	Shipment           Shipment
	PromoCode          PromoCode
	Buyer              Buyer
	Billing            Address
	Shipping           Address
	StatusHistory      []OrderStatusHistoryWithStatus
}

// Orders represents the orders table
type Order struct {
	Id             int             `db:"id"`
	UUID           string          `db:"uuid"`
	Placed         time.Time       `db:"placed"`
	Modified       time.Time       `db:"modified"`
	TotalPrice     decimal.Decimal `db:"total_price"`
	Currency       string          `db:"currency"` // ISO 4217 currency code
	OrderStatusId  int             `db:"order_status_id"`
	PromoId        sql.NullInt32   `db:"promo_id"`
	RefundReason   sql.NullString  `db:"refund_reason"`
	OrderComment   sql.NullString  `db:"order_comment"`
	RefundedAmount decimal.Decimal `db:"refunded_amount"`
}

func (o *Order) TotalPriceDecimal() decimal.Decimal {
	return currency.Round(o.TotalPrice, o.Currency)
}

func (o *Order) RefundedAmountDecimal() decimal.Decimal {
	return currency.Round(o.RefundedAmount, o.Currency)
}

type ProductInfoProvider interface {
	GetProductId() int
	GetProductPrice() decimal.Decimal
	GetProductSalePercentage() decimal.Decimal
	GetQuantity() decimal.Decimal
	GetSizeId() int
}

// OrderItem represents the order_item table
type OrderItem struct {
	Id            int                        `db:"id"`
	OrderId       int                        `db:"order_id"`
	Thumbnail     string                     `db:"thumbnail"`
	Translations  []ProductTranslationInsert `db:"translations"`
	BlurHash      string                     `db:"blur_hash"`
	ProductBrand  string                     `db:"product_brand"`
	Color         string                     `db:"color"`
	TopCategoryId int                        `db:"top_category_id"`
	SubCategoryId sql.NullInt32              `db:"sub_category_id"`
	TypeId        sql.NullInt32              `db:"type_id"`
	TargetGender  GenderEnum                 `db:"target_gender"`
	SKU           string                     `db:"product_sku"`
	Slug          string
	Preorder      sql.NullTime `db:"preorder"`
	OrderItemInsert
}

type OrderItemInsert struct {
	ProductId             int             `db:"product_id" valid:"required"`
	ProductPrice          decimal.Decimal `db:"product_price"`
	ProductSalePercentage decimal.Decimal `db:"product_sale_percentage"`
	ProductPriceWithSale  decimal.Decimal `db:"product_price_with_sale"`
	Quantity              decimal.Decimal `db:"quantity" valid:"required"`
	SizeId                int             `db:"size_id" valid:"required"`
}

func (oii *OrderItemInsert) ProductPriceWithSaleDecimal() decimal.Decimal {
	return oii.ProductPriceWithSale.Round(2)
}

func (oii *OrderItemInsert) ProductPriceDecimal() decimal.Decimal {
	return oii.ProductPrice.Round(2)
}

func (oii *OrderItemInsert) ProductSalePercentageDecimal() decimal.Decimal {
	return oii.ProductSalePercentage.Round(2)
}

func (oii *OrderItemInsert) QuantityDecimal() decimal.Decimal {
	return oii.Quantity.Round(0)
}

// OrderItemAdjustmentReason is a typed string for adjustment reasons.
type OrderItemAdjustmentReason string

func (r OrderItemAdjustmentReason) String() string {
	return string(r)
}

const (
	AdjustmentReasonOutOfStock      OrderItemAdjustmentReason = "out_of_stock"
	AdjustmentReasonQuantityReduced OrderItemAdjustmentReason = "quantity_reduced"
	AdjustmentReasonQuantityCapped  OrderItemAdjustmentReason = "quantity_capped"
)

// OrderItemAdjustment describes a change made during order item validation (e.g. quantity reduced, item removed).
type OrderItemAdjustment struct {
	ProductId         int
	SizeId            int
	RequestedQuantity decimal.Decimal
	AdjustedQuantity  decimal.Decimal // 0 means item was removed
	Reason            OrderItemAdjustmentReason
}

type OrderItemValidation struct {
	ValidItems      []OrderItem
	Subtotal        decimal.Decimal
	HasChanged      bool
	ItemAdjustments []OrderItemAdjustment
}

func (oiv *OrderItemValidation) SubtotalDecimal() decimal.Decimal {
	return oiv.Subtotal.Round(2)
}

// ByProductID implements sort.Interface for []OrderItemInsert based on the ProductID field.
type OrderItemsByProductId []OrderItemInsert

func (a OrderItemsByProductId) Len() int { return len(a) }
func (a OrderItemsByProductId) Less(i, j int) bool {
	if a[i].ProductId == a[j].ProductId {
		return a[i].SizeId < a[j].SizeId
	}
	return a[i].ProductId < a[j].ProductId
}
func (a OrderItemsByProductId) Swap(i, j int) { a[i], a[j] = a[j], a[i] }

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

func (oii *OrderItemInsert) GetProductId() int {
	return oii.ProductId
}

func (oii *OrderItemInsert) GetProductPrice() decimal.Decimal {
	return oii.ProductPriceDecimal()
}

func (oii *OrderItemInsert) GetProductSalePercentage() decimal.Decimal {
	return oii.ProductSalePercentageDecimal()
}

func (oii *OrderItemInsert) GetQuantity() decimal.Decimal {
	return oii.QuantityDecimal()
}
func (oii *OrderItemInsert) GetSizeId() int {
	return oii.SizeId
}

// OrderStatusName is the custom type to enforce enum-like behavior
type OrderStatusName string

func (osn *OrderStatusName) String() string {
	return string(*osn)
}

const (
	Placed            OrderStatusName = "placed"
	AwaitingPayment   OrderStatusName = "awaiting_payment"
	Confirmed         OrderStatusName = "confirmed"
	Shipped           OrderStatusName = "shipped"
	Delivered         OrderStatusName = "delivered"
	Cancelled         OrderStatusName = "cancelled"
	PendingReturn     OrderStatusName = "pending_return"
	RefundInProgress  OrderStatusName = "refund_in_progress"
	Refunded          OrderStatusName = "refunded"
	PartiallyRefunded OrderStatusName = "partially_refunded"
)

// ValidOrderStatusNames is a set of valid order status names
var ValidOrderStatusNames = map[OrderStatusName]bool{
	Placed:            true,
	AwaitingPayment:   true,
	Confirmed:         true,
	Shipped:           true,
	Delivered:         true,
	Cancelled:         true,
	PendingReturn:     true,
	RefundInProgress:  true,
	Refunded:          true,
	PartiallyRefunded: true,
}

// OrderStatus represents the order_status table
type OrderStatus struct {
	Id   int             `db:"id"`
	Name OrderStatusName `db:"name"`
}

type OrderBuyerShipment struct {
	Order    *Order
	Buyer    *Buyer
	Shipment *Shipment
}

// OrderStatusHistory represents the order_status_history table
type OrderStatusHistory struct {
	Id            int            `db:"id"`
	OrderId       int            `db:"order_id"`
	OrderStatusId int            `db:"order_status_id"`
	ChangedAt     time.Time      `db:"changed_at"`
	ChangedBy     sql.NullString `db:"changed_by"`
	Notes         sql.NullString `db:"notes"`
}

// OrderStatusHistoryWithStatus includes status name for API responses
type OrderStatusHistoryWithStatus struct {
	OrderStatusHistory
	StatusName OrderStatusName `db:"status_name"`
}

// ==================== Review Enums ====================

// ProductRating is the enum for product quality assessment
type ProductRating string

const (
	ProductRatingPoor      ProductRating = "poor"
	ProductRatingFair      ProductRating = "fair"
	ProductRatingGood      ProductRating = "good"
	ProductRatingVeryGood  ProductRating = "very_good"
	ProductRatingExcellent ProductRating = "excellent"
)

// ValidProductRatings is a set of valid product rating values
var ValidProductRatings = map[ProductRating]bool{
	ProductRatingPoor:      true,
	ProductRatingFair:      true,
	ProductRatingGood:      true,
	ProductRatingVeryGood:  true,
	ProductRatingExcellent: true,
}

// FitScale is the enum for fit assessment
type FitScale string

const (
	FitScaleRunsSmall     FitScale = "runs_small"
	FitScaleSlightlySmall FitScale = "slightly_small"
	FitScaleTrueToSize    FitScale = "true_to_size"
	FitScaleSlightlyLarge FitScale = "slightly_large"
	FitScaleRunsLarge     FitScale = "runs_large"
)

// ValidFitScales is a set of valid fit scale values
var ValidFitScales = map[FitScale]bool{
	FitScaleRunsSmall:     true,
	FitScaleSlightlySmall: true,
	FitScaleTrueToSize:    true,
	FitScaleSlightlyLarge: true,
	FitScaleRunsLarge:     true,
}

// DeliverySpeed is the enum for delivery experience
type DeliverySpeed string

const (
	DeliverySpeedMuchFaster DeliverySpeed = "much_faster_than_expected"
	DeliverySpeedFaster     DeliverySpeed = "faster_than_expected"
	DeliverySpeedAsExpected DeliverySpeed = "as_expected"
	DeliverySpeedSlower     DeliverySpeed = "slower_than_expected"
	DeliverySpeedMuchSlower DeliverySpeed = "much_slower_than_expected"
)

// ValidDeliverySpeeds is a set of valid delivery speed values
var ValidDeliverySpeeds = map[DeliverySpeed]bool{
	DeliverySpeedMuchFaster: true,
	DeliverySpeedFaster:     true,
	DeliverySpeedAsExpected: true,
	DeliverySpeedSlower:     true,
	DeliverySpeedMuchSlower: true,
}

// PackagingCondition is the enum for packaging quality
type PackagingCondition string

const (
	PackagingConditionDamaged    PackagingCondition = "damaged"
	PackagingConditionAcceptable PackagingCondition = "acceptable"
	PackagingConditionGood       PackagingCondition = "good"
	PackagingConditionExcellent  PackagingCondition = "excellent"
)

// ValidPackagingConditions is a set of valid packaging condition values
var ValidPackagingConditions = map[PackagingCondition]bool{
	PackagingConditionDamaged:    true,
	PackagingConditionAcceptable: true,
	PackagingConditionGood:       true,
	PackagingConditionExcellent:  true,
}

// ==================== Review Entities ====================

// OrderReview represents the order_review table (order-level: delivery & packaging)
type OrderReview struct {
	Id              int                `db:"id"`
	OrderId         int                `db:"order_id"`
	DeliveryRating  DeliverySpeed      `db:"delivery_rating"`
	PackagingRating PackagingCondition `db:"packaging_rating"`
	CreatedAt       time.Time          `db:"created_at"`
}

// OrderReviewInsert is the input for creating an order-level review
type OrderReviewInsert struct {
	DeliveryRating  DeliverySpeed      `db:"delivery_rating"`
	PackagingRating PackagingCondition `db:"packaging_rating"`
}

// OrderItemReview represents the order_item_review table (item-level)
type OrderItemReview struct {
	Id          int           `db:"id"`
	OrderItemId int           `db:"order_item_id"`
	Rating      ProductRating `db:"rating"`
	FitRating   FitScale      `db:"fit_rating"`
	Recommend   bool          `db:"recommend"`
	Text        string        `db:"text"`
	CreatedAt   time.Time     `db:"created_at"`
}

// OrderItemReviewInsert is the input for creating an item-level review
type OrderItemReviewInsert struct {
	OrderItemId int           `db:"order_item_id"`
	Rating      ProductRating `db:"rating"`
	FitRating   FitScale      `db:"fit_rating"`
	Recommend   bool          `db:"recommend"`
	Text        string        `db:"text"`
}

// OrderReviewFull combines order-level and item-level reviews
type OrderReviewFull struct {
	OrderReview OrderReview
	ItemReviews []OrderItemReview
}

// ValidateOrderReviewInsert validates the order-level review input
func ValidateOrderReviewInsert(r *OrderReviewInsert) error {
	if !ValidDeliverySpeeds[r.DeliveryRating] {
		return &ValidationError{Message: "invalid delivery_rating value"}
	}
	if !ValidPackagingConditions[r.PackagingRating] {
		return &ValidationError{Message: "invalid packaging_rating value"}
	}
	return nil
}

// ValidateOrderItemReviewInsert validates the item-level review input
func ValidateOrderItemReviewInsert(r *OrderItemReviewInsert) error {
	if r.OrderItemId <= 0 {
		return &ValidationError{Message: "order_item_id is required"}
	}
	if !ValidProductRatings[r.Rating] {
		return &ValidationError{Message: "invalid rating value"}
	}
	if !ValidFitScales[r.FitRating] {
		return &ValidationError{Message: "invalid fit_rating value"}
	}
	if r.Text == "" {
		return &ValidationError{Message: "text is required"}
	}
	if len(r.Text) > 2000 {
		return &ValidationError{Message: "text must not exceed 2000 characters"}
	}
	return nil
}

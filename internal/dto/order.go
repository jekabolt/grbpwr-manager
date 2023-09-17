// Package dto contains data transfer objects for orders.
package dto

import (
	"fmt"
	"time"

	common_pb "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
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

func (pm *PaymentMethod) ConvertToProtoPaymentMethod() (common_pb.PaymentMethod, error) {
	switch *pm {
	case CardPayment:
		return *common_pb.PaymentMethod_CARD.Enum(), nil
	case Ethereum:
		return *common_pb.PaymentMethod_ETHEREUM.Enum(), nil
	case USDC:
		return *common_pb.PaymentMethod_USDC.Enum(), nil
	case USDT:
		return *common_pb.PaymentMethod_USDT.Enum(), nil
	default:
		return 0, fmt.Errorf("unknown payment method: %v", pm)
	}
}

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

func (pc *PaymentCurrency) ConvertToProtoPaymentCurrency() (common_pb.PaymentCurrency, error) {
	switch *pc {
	case EUR:
		return *common_pb.PaymentCurrency_EUR.Enum(), nil
	case USD:
		return *common_pb.PaymentCurrency_USD.Enum(), nil
	case USDCrypto:
		return *common_pb.PaymentCurrency_USDCRYPTO.Enum(), nil
	case ETH:
		return *common_pb.PaymentCurrency_ETH.Enum(), nil
	default:
		return 0, fmt.Errorf("unknown payment currency: %s", pc)
	}
}

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

func ConvertProtoOrderStatus(os *common_pb.OrderStatus) (OrderStatus, error) {
	switch *os {
	case common_pb.OrderStatus_PLACED:
		return OrderPlaced, nil
	case common_pb.OrderStatus_CONFIRMED:
		return OrderConfirmed, nil
	case common_pb.OrderStatus_SHIPPED:
		return OrderShipped, nil
	case common_pb.OrderStatus_DELIVERED:
		return OrderDelivered, nil
	case common_pb.OrderStatus_CANCELLED:
		return OrderCancelled, nil
	case common_pb.OrderStatus_REFUNDED:
		return OrderRefunded, nil
	default:
		return "", fmt.Errorf("unknown order status: %s", os)
	}
}

func (pm *OrderStatus) ConvertToProtoOrderStatus() (common_pb.OrderStatus, error) {
	switch *pm {
	case OrderPlaced:
		return *common_pb.OrderStatus_PLACED.Enum(), nil
	case OrderConfirmed:
		return *common_pb.OrderStatus_CONFIRMED.Enum(), nil
	case OrderShipped:
		return *common_pb.OrderStatus_SHIPPED.Enum(), nil
	case OrderDelivered:
		return *common_pb.OrderStatus_DELIVERED.Enum(), nil
	case OrderCancelled:
		return *common_pb.OrderStatus_CANCELLED.Enum(), nil
	case OrderRefunded:
		return *common_pb.OrderStatus_REFUNDED.Enum(), nil
	default:
		return 0, fmt.Errorf("unknown order status: %v", pm)
	}
}

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

func (a *Address) ConvertToProtoAddress() *common_pb.Address {
	return &common_pb.Address{
		Id:              a.ID,
		Street:          a.Street,
		HouseNumber:     a.HouseNumber,
		ApartmentNumber: a.ApartmentNumber,
		City:            a.City,
		State:           a.State,
		Country:         a.Country,
		PostalCode:      a.PostalCode,
	}
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

// type Shipment struct {
// 	ID                   int32     `db:"id"`
// 	CarrierId            int32     `db:"carrier_id"`
// 	TrackingCode         string    `db:"tracking_code"`
// 	ShippingDate         time.Time `db:"shipping_date"`
// 	EstimatedArrivalDate time.Time `db:"estimated_arrival_date"`
// }

// Order represents a purchase order.
type Order struct {
	ID         int32 `db:"id"`
	Buyer      *Buyer
	Placed     time.Time
	Items      []Item
	Payment    *Payment
	Shipment   *Shipment
	TotalPrice decimal.Decimal
	PromoCode  *PromoCode
	Status     OrderStatus
}

func (o *Order) ConvertToProtoOrder() (*common_pb.Order, error) {

	items := make([]*common_pb.Item, 0, len(o.Items))
	for _, item := range o.Items {
		items = append(items, item.ConvertToProtoAddress())
	}

	pm, err := o.Payment.Method.ConvertToProtoPaymentMethod()
	if err != nil {
		return nil, err
	}

	pc, err := o.Payment.Currency.ConvertToProtoPaymentCurrency()
	if err != nil {
		return nil, err
	}

	amount, err := decimal.NewFromString(o.Payment.TransactionAmount.String())
	if err != nil {
		return nil, err
	}
	os, err := o.Status.ConvertToProtoOrderStatus()
	if err != nil {
		return nil, err
	}

	return &common_pb.Order{
		Id: int64(o.ID),
		Buyer: &common_pb.Buyer{
			Id:                 o.Buyer.ID,
			FirstName:          o.Buyer.FirstName,
			LastName:           o.Buyer.LastName,
			Email:              o.Buyer.Email,
			Phone:              o.Buyer.Phone,
			BillingAddress:     o.Buyer.BillingAddress.ConvertToProtoAddress(),
			ShippingAddress:    o.Buyer.ShippingAddress.ConvertToProtoAddress(),
			ReceivePromoEmails: o.Buyer.ReceivePromoEmails,
		},
		Placed: timestamppb.New(o.Placed),
		Items:  items,
		Payment: &common_pb.Payment{
			Id:                o.Payment.ID,
			Method:            pm,
			Currency:          pc,
			TransactionId:     o.Payment.TransactionID,
			TransactionAmount: amount.String(),
			Payer:             o.Payment.Payer,
			Payee:             o.Payment.Payee,
			IsTransactionDone: o.Payment.IsTransactionDone,
		},
		Shipment: &common_pb.Shipment{
			Id:                   o.Shipment.ID,
			Carrier:              o.Shipment.Carrier,
			Cost:                 o.Shipment.Cost.String(),
			TrackingCode:         o.Shipment.TrackingCode,
			ShippingDate:         timestamppb.New(o.Shipment.ShippingDate),
			EstimatedArrivalDate: timestamppb.New(o.Shipment.EstimatedArrivalDate),
		},
		TotalPrice: o.TotalPrice.String(),
		Status:     os,
	}, nil
}

type Item struct {
	// ID is the ID of the product.
	ID int32
	// Quantity is the number of items of this product in the order in corresponding size.
	Quantity int
	// Size is the size of the product in the order.
	Size string
}

func (i *Item) ConvertToProtoAddress() *common_pb.Item {
	return &common_pb.Item{
		Id:       int64(i.ID),
		Quantity: int32(i.Quantity),
		Size:     i.Size,
	}
}

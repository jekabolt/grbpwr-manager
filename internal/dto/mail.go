package dto

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
	"github.com/shopspring/decimal"
)

func OrderFullToOrderConfirmed(of *entity.OrderFull, sizeMap map[int]entity.Size, shippingCarriersMap map[int]entity.ShipmentCarrier) *OrderConfirmed {
	oi := EntityOrderItemsToDto(of.OrderItems, sizeMap)
	sc, ok := shippingCarriersMap[of.Shipment.CarrierID]
	if !ok {
		sc = entity.ShipmentCarrier{
			ShipmentCarrierInsert: entity.ShipmentCarrierInsert{
				Price:   decimal.Zero,
				Carrier: "unknown",
			},
		}
	}
	return &OrderConfirmed{
		OrderUUID:           of.Order.UUID,
		TotalPrice:          of.Order.TotalPrice.String(),
		OrderItems:          oi,
		FullName:            fmt.Sprintf("%s %s", of.Buyer.FirstName, of.Buyer.LastName),
		PromoExist:          of.PromoCode.ID != 0,
		PromoDiscountAmount: of.PromoCode.Discount.String(),
		HasFreeShipping:     of.PromoCode.FreeShipping,
		ShippingPrice:       sc.Price.String(),
		ShipmentCarrier:     sc.Carrier,
	}
}

func EntityOrderItemsToDto(items []entity.OrderItem, sizeMap map[int]entity.Size) []OrderItem {
	oi := make([]OrderItem, len(items))
	for i, item := range items {
		size, found := sizeMap[item.SizeID]
		if !found {
			size = entity.Size{
				Name: "unknown",
			}
		}
		oi[i] = OrderItem{
			Name:        fmt.Sprintf("%s %s", item.ProductBrand, item.ProductName),
			Thumbnail:   item.Thumbnail,
			Size:        string(size.Name),
			Quantity:    int(item.Quantity.IntPart()),
			Price:       item.OrderItemInsert.ProductPrice.String(),
			SalePercent: item.OrderItemInsert.ProductSalePercentage.String(),
		}
	}

	return oi
}

type OrderConfirmed struct {
	OrderUUID           string
	TotalPrice          string
	OrderItems          []OrderItem
	FullName            string
	PromoExist          bool
	PromoDiscountAmount string
	HasFreeShipping     bool
	ShippingPrice       string
	ShipmentCarrier     string
}

type OrderItem struct {
	Name        string
	Thumbnail   string
	Size        string
	Quantity    int
	Price       string
	SalePercent string
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
	OrderUUID      string
	ShippingDate   string
	TrackingNumber string
	TrackingURL    string
}
type PromoCodeDetails struct {
	PromoCode       string
	HasFreeShipping bool
	DiscountAmount  int
	ExpirationDate  string
}

func ResendSendEmailRequestToEntity(mr *resend.SendEmailRequest, to string) *entity.SendEmailRequest {
	return &entity.SendEmailRequest{
		From:    mr.From,
		To:      to,
		Html:    *mr.Html,
		Subject: mr.Subject,
		ReplyTo: *mr.ReplyTo,
	}
}

func EntitySendEmailRequestToResend(mr *entity.SendEmailRequest) (*resend.SendEmailRequest, error) {
	if mr.To == "" {
		return nil, fmt.Errorf("mail req 'to' is empty")
	}
	return &resend.SendEmailRequest{
		From:    mr.From,
		To:      []string{mr.To},
		Html:    &mr.Html,
		Subject: mr.Subject,
		ReplyTo: &mr.ReplyTo,
	}, nil
}

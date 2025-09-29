package dto

import (
	"encoding/base64"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
	"github.com/shopspring/decimal"
)

func OrderFullToOrderConfirmed(of *entity.OrderFull) *OrderConfirmed {
	sc, ok := cache.GetShipmentCarrierById(of.Shipment.CarrierId)
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
		TotalPrice:          of.Order.TotalPriceDecimal().String(),
		OrderItems:          EntityOrderItemsToDto(of.OrderItems),
		FullName:            fmt.Sprintf("%s %s", of.Buyer.FirstName, of.Buyer.LastName),
		PromoExist:          of.PromoCode.Id != 0,
		PromoDiscountAmount: of.PromoCode.DiscountDecimal().String(),
		HasFreeShipping:     of.PromoCode.FreeShipping,
		ShippingPrice:       sc.PriceDecimal().String(),
		ShipmentCarrier:     sc.Carrier,
		EmailB64:            base64.StdEncoding.EncodeToString([]byte(of.Buyer.Email)),
	}
}

func EntityOrderItemsToDto(items []entity.OrderItem) []OrderItem {
	oi := make([]OrderItem, len(items))
	for i, item := range items {
		size, found := cache.GetSizeById(item.SizeId)
		if !found {
			size = entity.Size{
				Name: "unknown",
			}
		}
		// Get product name from first translation or use default
		productName := "Product"
		if len(item.Translations) > 0 {
			productName = item.Translations[0].Name
		}

		oi[i] = OrderItem{
			Name:        fmt.Sprintf("%s %s", item.ProductBrand, productName),
			Thumbnail:   item.Thumbnail,
			Size:        size.Name,
			Quantity:    int(item.Quantity.IntPart()),
			Price:       item.OrderItemInsert.ProductPriceDecimal().String(),
			SalePercent: item.OrderItemInsert.ProductSalePercentageDecimal().String(),
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
	EmailB64            string
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
	Name         string
	OrderUUID    string
	ShippingDate string
}
type PromoCodeDetails struct {
	PromoCode       string
	HasFreeShipping bool
	DiscountAmount  int
	ExpirationDate  string
}

func ResendSendEmailRequestToEntity(mr *resend.SendEmailRequest) (*entity.SendEmailRequest, error) {
	if len(mr.To) == 0 {
		return nil, fmt.Errorf("mail req 'to' is empty")
	}
	return &entity.SendEmailRequest{
		From:    mr.From,
		To:      mr.To[0],
		Html:    *mr.Html,
		Subject: mr.Subject,
		ReplyTo: *mr.ReplyTo,
	}, nil
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

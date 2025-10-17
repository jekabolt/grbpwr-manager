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

	// Calculate subtotal (total - shipping)
	subtotal := of.Order.TotalPriceDecimal().Sub(sc.PriceDecimal())

	return &OrderConfirmed{
		Preheader:           "YOUR GRBPWR ORDER HAS BEEN CONFIRMED",
		OrderUUID:           of.Order.UUID,
		SubtotalPrice:       subtotal.String(),
		TotalPrice:          of.Order.TotalPriceDecimal().String(),
		OrderItems:          EntityOrderItemsToDto(of.OrderItems),
		PromoExist:          of.PromoCode.Id != 0,
		PromoDiscountAmount: of.PromoCode.DiscountDecimal().String(),
		HasFreeShipping:     of.PromoCode.FreeShipping,
		ShippingPrice:       sc.PriceDecimal().String(),
		EmailB64:            base64.StdEncoding.EncodeToString([]byte(of.Buyer.Email)),
	}
}

func OrderFullToOrderShipment(of *entity.OrderFull) *OrderShipment {
	sc, ok := cache.GetShipmentCarrierById(of.Shipment.CarrierId)
	if !ok {
		sc = entity.ShipmentCarrier{
			ShipmentCarrierInsert: entity.ShipmentCarrierInsert{
				Price:   decimal.Zero,
				Carrier: "unknown",
			},
		}
	}

	// Calculate subtotal (total - shipping)
	subtotal := of.Order.TotalPriceDecimal().Sub(sc.PriceDecimal())

	return &OrderShipment{
		Preheader:           "YOUR GRBPWR ORDER HAS BEEN SHIPPED",
		OrderUUID:           of.Order.UUID,
		EmailB64:            base64.StdEncoding.EncodeToString([]byte(of.Buyer.Email)),
		OrderItems:          EntityOrderItemsToDto(of.OrderItems),
		SubtotalPrice:       subtotal.String(),
		TotalPrice:          of.Order.TotalPriceDecimal().String(),
		PromoExist:          of.PromoCode.Id != 0,
		PromoDiscountAmount: of.PromoCode.DiscountDecimal().String(),
		HasFreeShipping:     of.PromoCode.FreeShipping,
		ShippingPrice:       sc.PriceDecimal().String(),
	}
}

func OrderFullToOrderCancelled(of *entity.OrderFull) *OrderCancelled {
	return &OrderCancelled{
		Preheader: "YOUR GRBPWR ORDER HAS BEEN CANCELLED",
		OrderUUID: of.Order.UUID,
		EmailB64:  base64.StdEncoding.EncodeToString([]byte(of.Buyer.Email)),
	}
}

func OrderFullToOrderRefundInitiated(of *entity.OrderFull) *OrderRefundInitiated {
	return &OrderRefundInitiated{
		Preheader: "YOUR GRBPWR REFUND HAS BEEN INITIATED",
		OrderUUID: of.Order.UUID,
		EmailB64:  base64.StdEncoding.EncodeToString([]byte(of.Buyer.Email)),
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
			Name:      fmt.Sprintf("%s %s", item.ProductBrand, productName),
			Thumbnail: item.Thumbnail,
			Size:      size.Name,
			Quantity:  int(item.Quantity.IntPart()),
			Price:     item.OrderItemInsert.ProductPriceDecimal().String(),
		}
	}

	return oi
}

type OrderConfirmed struct {
	Preheader           string
	OrderUUID           string
	SubtotalPrice       string
	TotalPrice          string
	OrderItems          []OrderItem
	PromoExist          bool
	PromoDiscountAmount string
	HasFreeShipping     bool
	ShippingPrice       string
	EmailB64            string
}

type OrderItem struct {
	Name      string
	Thumbnail string
	Size      string
	Quantity  int
	Price     string
}

type OrderCancelled struct {
	Preheader string
	OrderUUID string
	EmailB64  string
}

type OrderShipment struct {
	Preheader           string
	OrderUUID           string
	EmailB64            string
	OrderItems          []OrderItem
	SubtotalPrice       string
	TotalPrice          string
	PromoExist          bool
	PromoDiscountAmount string
	HasFreeShipping     bool
	ShippingPrice       string
}

type OrderRefundInitiated struct {
	Preheader string
	OrderUUID string
	EmailB64  string
}

type PromoCodeDetails struct {
	Preheader      string
	PromoCode      string
	DiscountAmount int
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

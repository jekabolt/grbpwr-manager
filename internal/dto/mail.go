package dto

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"

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
				Carrier: "unknown",
			},
		}
	}

	// Get shipping price for the order's currency
	shippingPrice, err := sc.PriceDecimal(of.Order.Currency)
	if err != nil {
		shippingPrice = decimal.Zero
	}

	// Calculate subtotal (total - shipping)
	subtotal := of.Order.TotalPriceDecimal().Sub(shippingPrice)

	// Build buyer name (first name, or first + last if both available)
	buyerName := of.Buyer.FirstName
	if of.Buyer.LastName != "" {
		buyerName = fmt.Sprintf("%s %s", of.Buyer.FirstName, of.Buyer.LastName)
	}

	return &OrderConfirmed{
		Preheader:           "YOUR GRBPWR ORDER HAS BEEN CONFIRMED",
		BuyerName:           buyerName,
		OrderUUID:           of.Order.UUID,
		CurrencySymbol:      CurrencySymbol(of.Order.Currency),
		SubtotalPrice:       subtotal.String(),
		TotalPrice:          of.Order.TotalPriceDecimal().String(),
		OrderItems:          EntityOrderItemsToDto(of.OrderItems, of.Order.Currency),
		PromoExist:          of.PromoCode.Id != 0,
		PromoDiscountAmount: of.PromoCode.DiscountDecimal().String(),
		HasFreeShipping:     of.PromoCode.FreeShipping,
		ShippingPrice:       shippingPrice.String(),
		EmailB64:            base64.StdEncoding.EncodeToString([]byte(of.Buyer.Email)),
	}
}

func OrderFullToOrderShipment(of *entity.OrderFull) *OrderShipment {
	sc, ok := cache.GetShipmentCarrierById(of.Shipment.CarrierId)
	if !ok {
		sc = entity.ShipmentCarrier{
			ShipmentCarrierInsert: entity.ShipmentCarrierInsert{
				Carrier: "unknown",
			},
		}
	}

	// Get shipping price for the order's currency
	shippingPrice, err := sc.PriceDecimal(of.Order.Currency)
	if err != nil {
		shippingPrice = decimal.Zero
	}

	// Calculate subtotal (total - shipping)
	subtotal := of.Order.TotalPriceDecimal().Sub(shippingPrice)

	// Build buyer name (first name, or first + last if both available)
	buyerName := of.Buyer.FirstName
	if of.Buyer.LastName != "" {
		buyerName = fmt.Sprintf("%s %s", of.Buyer.FirstName, of.Buyer.LastName)
	}

	return &OrderShipment{
		Preheader:           "YOUR GRBPWR ORDER HAS BEEN SHIPPED",
		BuyerName:           buyerName,
		OrderUUID:           of.Order.UUID,
		CurrencySymbol:      CurrencySymbol(of.Order.Currency),
		EmailB64:            base64.StdEncoding.EncodeToString([]byte(of.Buyer.Email)),
		OrderItems:          EntityOrderItemsToDto(of.OrderItems, of.Order.Currency),
		SubtotalPrice:       subtotal.String(),
		TotalPrice:          of.Order.TotalPriceDecimal().String(),
		PromoExist:          of.PromoCode.Id != 0,
		PromoDiscountAmount: of.PromoCode.DiscountDecimal().String(),
		HasFreeShipping:     of.PromoCode.FreeShipping,
		ShippingPrice:       shippingPrice.String(),
	}
}

// OrderFullToOrderDelivered builds the delivered-email data. It reuses the shipment builder (same
// item/total layout) and only changes the preheader.
func OrderFullToOrderDelivered(of *entity.OrderFull) *OrderDelivered {
	s := OrderFullToOrderShipment(of)
	return &OrderDelivered{
		Preheader:           "YOUR GRBPWR ORDER HAS BEEN DELIVERED",
		BuyerName:           s.BuyerName,
		OrderUUID:           s.OrderUUID,
		CurrencySymbol:      s.CurrencySymbol,
		EmailB64:            s.EmailB64,
		OrderItems:          s.OrderItems,
		SubtotalPrice:       s.SubtotalPrice,
		TotalPrice:          s.TotalPrice,
		PromoExist:          s.PromoExist,
		PromoDiscountAmount: s.PromoDiscountAmount,
		HasFreeShipping:     s.HasFreeShipping,
		ShippingPrice:       s.ShippingPrice,
	}
}

func OrderFullToOrderCancelled(of *entity.OrderFull) *OrderCancelled {
	// Build buyer name (first name, or first + last if both available)
	buyerName := of.Buyer.FirstName
	if of.Buyer.LastName != "" {
		buyerName = fmt.Sprintf("%s %s", of.Buyer.FirstName, of.Buyer.LastName)
	}

	return &OrderCancelled{
		Preheader: "YOUR GRBPWR ORDER HAS BEEN CANCELLED",
		BuyerName: buyerName,
		OrderUUID: of.Order.UUID,
		EmailB64:  base64.StdEncoding.EncodeToString([]byte(of.Buyer.Email)),
	}
}

func OrderFullToOrderRefundInitiated(of *entity.OrderFull) *OrderRefundInitiated {
	// Build buyer name (first name, or first + last if both available)
	buyerName := of.Buyer.FirstName
	if of.Buyer.LastName != "" {
		buyerName = fmt.Sprintf("%s %s", of.Buyer.FirstName, of.Buyer.LastName)
	}

	return &OrderRefundInitiated{
		Preheader: "YOUR GRBPWR REFUND HAS BEEN INITIATED",
		BuyerName: buyerName,
		OrderUUID: of.Order.UUID,
		EmailB64:  base64.StdEncoding.EncodeToString([]byte(of.Buyer.Email)),
	}
}

func OrderFullToOrderPendingReturn(of *entity.OrderFull) *OrderPendingReturn {
	buyerName := of.Buyer.FirstName
	if of.Buyer.LastName != "" {
		buyerName = fmt.Sprintf("%s %s", of.Buyer.FirstName, of.Buyer.LastName)
	}

	return &OrderPendingReturn{
		Preheader: "YOUR GRBPWR RETURN HAS BEEN REQUESTED",
		BuyerName: buyerName,
		OrderUUID: of.Order.UUID,
		EmailB64:  base64.StdEncoding.EncodeToString([]byte(of.Buyer.Email)),
	}
}

// tailoredSizeNameRe matches internal compound size codes of the form
// label_measurement{ta|bo}_{m|f}, e.g. "xs_44ta_m" (tailored, chest 44) or
// "xxs_23bo_f" (bottoms, 23" waist). See migrations 0018/0019.
var tailoredSizeNameRe = regexp.MustCompile(`^([a-z]+)_(\d+(?:\.\d+)?)(?:ta|bo)_[mf]$`)

// FormatSizeName turns an internal size code into a human-readable label for
// customer-facing emails. Compound tailored/bottoms codes become
// "<LABEL> · <measurement>" (e.g. "xs_44ta_m" → "XS · 44"); plain letter sizes
// ("m"), shoe sizes ("42") and anything unrecognised pass through unchanged.
func FormatSizeName(name string) string {
	m := tailoredSizeNameRe.FindStringSubmatch(name)
	if m == nil {
		return name
	}
	return strings.ToUpper(m[1]) + " · " + m[2]
}

func EntityOrderItemsToDto(items []entity.OrderItem, currency string) []OrderItem {
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
			Size:      FormatSizeName(size.Name),
			Quantity:  int(item.Quantity.IntPart()),
			Price:     RoundForCurrency(item.OrderItemInsert.ProductPriceWithSale, currency).String(),
		}
	}

	return oi
}

type OrderConfirmed struct {
	Preheader           string
	BuyerName           string // First name or full name if available
	OrderUUID           string
	CurrencySymbol      string
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
	BuyerName string // First name or full name if available
	OrderUUID string
	EmailB64  string
}

type OrderShipment struct {
	Preheader           string
	BuyerName           string // First name or full name if available
	OrderUUID           string
	CurrencySymbol      string
	EmailB64            string
	OrderItems          []OrderItem
	SubtotalPrice       string
	TotalPrice          string
	PromoExist          bool
	PromoDiscountAmount string
	HasFreeShipping     bool
	ShippingPrice       string
}

// OrderDelivered carries the data for the "order delivered" email. It mirrors OrderShipment (same
// item/total layout) but is a distinct type so the subject line and template resolve correctly.
type OrderDelivered struct {
	Preheader           string
	BuyerName           string // First name or full name if available
	OrderUUID           string
	CurrencySymbol      string
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
	BuyerName string // First name or full name if available
	OrderUUID string
	EmailB64  string
}

type OrderPendingReturn struct {
	Preheader string
	BuyerName string
	OrderUUID string
	EmailB64  string
}

type PromoCodeDetails struct {
	Preheader      string
	BuyerName      string // First name or full name if available
	PromoCode      string
	DiscountAmount int
	EmailB64       string
}

// CreatePromoCodeDetails creates a PromoCodeDetails DTO with EmailB64 set
func CreatePromoCodeDetails(preheader, buyerName, promoCode, email string, discountAmount int) *PromoCodeDetails {
	return &PromoCodeDetails{
		Preheader:      preheader,
		BuyerName:      buyerName,
		PromoCode:      promoCode,
		DiscountAmount: discountAmount,
		EmailB64:       base64.StdEncoding.EncodeToString([]byte(email)),
	}
}

func ResendSendEmailRequestToEntity(mr *resend.SendEmailRequest) (*entity.SendEmailRequest, error) {
	if len(mr.To) == 0 {
		return nil, fmt.Errorf("mail req 'to' is empty")
	}
	var html, replyTo string
	if mr.Html != nil {
		html = *mr.Html
	}
	if mr.ReplyTo != nil {
		replyTo = *mr.ReplyTo
	}
	return &entity.SendEmailRequest{
		From:    mr.From,
		To:      mr.To[0],
		Html:    html,
		Subject: mr.Subject,
		ReplyTo: replyTo,
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

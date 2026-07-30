package dto

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/canonical"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/localeutil"
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
		Locale:              of.Order.Locale.String,
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
		Locale:              of.Order.Locale.String,
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
// item/total layout); the distinct type is what selects the delivered subject and template.
func OrderFullToOrderDelivered(of *entity.OrderFull) *OrderDelivered {
	s := OrderFullToOrderShipment(of)
	return &OrderDelivered{
		Locale:              of.Order.Locale.String,
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
		Locale:    of.Order.Locale.String,
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
		Locale:    of.Order.Locale.String,
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
		Locale:    of.Order.Locale.String,
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
	langs := cache.GetLanguages()
	// languageId -> canonical email locale (cn->zh, kr->ko); languages that don't map to a
	// supported email locale drop out, so they never key a LocalizedNames entry.
	localeByLangID := make(map[int]string, len(langs))
	for _, l := range langs {
		if code := localeutil.Canonical(l.Code); code != "" {
			localeByLangID[l.Id] = code
		}
	}

	oi := make([]OrderItem, len(items))
	for i, item := range items {
		size, found := cache.GetSizeById(item.SizeId)
		if !found {
			size = entity.Size{
				Name: "unknown",
			}
		}
		// Default-language product name (deterministic canonical pick) — the fallback used
		// for English renders and for any recipient locale that has no translation.
		productName := "Product"
		if name, ok := canonical.ProductName(item.Translations, langs); ok {
			productName = name
		}

		// Per-locale "Brand Name" so the mailer can render each order line in the recipient's
		// resolved email locale (see the localName template func); locales absent from this map
		// fall back to Name above.
		var localizedNames map[string]string
		for _, tr := range item.Translations {
			code, ok := localeByLangID[tr.LanguageId]
			if !ok {
				continue
			}
			if localizedNames == nil {
				localizedNames = make(map[string]string, len(item.Translations))
			}
			localizedNames[code] = fmt.Sprintf("%s %s", item.ProductBrand, tr.Name)
		}

		oi[i] = OrderItem{
			Name:           fmt.Sprintf("%s %s", item.ProductBrand, productName),
			LocalizedNames: localizedNames,
			Thumbnail:      item.Thumbnail,
			Size:           FormatSizeName(size.Name),
			Quantity:       int(item.Quantity.IntPart()),
			Price:          RoundForCurrency(item.OrderItemInsert.ProductPriceWithSale, currency).String(),
		}
	}

	return oi
}

type OrderConfirmed struct {
	Locale              string // recipient-locale hint captured at purchase (order.locale)
	Preheader           string // unused: the preview line is localized in the template, from <template>.preheader
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
	Name string
	// LocalizedNames maps a canonical email locale (en,fr,de,it,ja,zh,ko) to the
	// "Brand Name" for that locale. The mailer picks the recipient's resolved-locale
	// entry at render time (localName func); a missing locale falls back to Name.
	LocalizedNames map[string]string
	Thumbnail      string
	Size           string
	Quantity       int
	Price          string
}

type OrderCancelled struct {
	Locale    string // recipient-locale hint captured at purchase (order.locale)
	Preheader string // unused, see OrderConfirmed.Preheader
	BuyerName string // First name or full name if available
	OrderUUID string
	EmailB64  string
}

type OrderShipment struct {
	Locale              string // recipient-locale hint captured at purchase (order.locale)
	Preheader           string // unused, see OrderConfirmed.Preheader
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
	Locale              string // recipient-locale hint captured at purchase (order.locale)
	Preheader           string // unused, see OrderConfirmed.Preheader
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
	Locale    string // recipient-locale hint captured at purchase (order.locale)
	Preheader string // unused, see OrderConfirmed.Preheader
	BuyerName string // First name or full name if available
	OrderUUID string
	EmailB64  string
}

type OrderPendingReturn struct {
	Locale    string // recipient-locale hint captured at purchase (order.locale)
	Preheader string // unused, see OrderConfirmed.Preheader
	BuyerName string
	OrderUUID string
	EmailB64  string
}

type PromoCodeDetails struct {
	Preheader      string // unused, see OrderConfirmed.Preheader
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

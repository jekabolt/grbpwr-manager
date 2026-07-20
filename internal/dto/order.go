// Package dto contains data transfer objects for orders.
package dto

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// buyerVatIDPattern is an approximate EU/UK VAT identifier format check for
// CreateCustomOrderRequest.buyer_vat_id (phase 2, wave 1): a 2-letter country prefix followed by
// 8 to 12 alphanumeric characters. VIES verification is explicitly out of scope (07 §7.4.3) — this
// is a free-form field with format validation only, to catch typos, not to prove the id is real.
//
// The length range (8..12 after the prefix) mirrors plan §1.3 ("префикс страны + 8–12 знаков") and
// real-world EU VAT id lengths (DE 9 digits, PL/IT 10-11, FR 11, NL 12 incl. a literal "B..."
// suffix). Input is upper-cased before matching, so the pattern only needs [A-Z0-9].
var buyerVatIDPattern = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{8,12}$`)

// convertBuyerVatID validates and normalises CreateCustomOrderRequest.buyer_vat_id: empty is valid
// (a B2C custom order — BuyerVatID stays NULL); non-empty must match buyerVatIDPattern once
// whitespace-stripped and upper-cased, else InvalidArgument (surfaced by the caller). Its presence is
// what makes an order B2B downstream (wdt / 4310 wholesale revenue — see entity.OrderNew.BuyerVatID).
//
// D-6: ALL whitespace is removed (not merely trimmed) before matching, so an operator pasting an id
// with internal spaces — "DE 123 456 789" — normalises to "DE123456789" instead of being rejected.
func convertBuyerVatID(raw string) (sql.NullString, error) {
	s := strings.ToUpper(strings.Join(strings.Fields(raw), ""))
	if s == "" {
		return sql.NullString{}, nil
	}
	if !buyerVatIDPattern.MatchString(s) {
		return sql.NullString{}, fmt.Errorf("invalid buyer_vat_id %q: expected a 2-letter country prefix followed by 8-12 alphanumeric characters", raw)
	}
	return sql.NullString{String: s, Valid: true}, nil
}

// ConvertPbOrderItemToEntity converts a protobuf OrderItem to an entity OrderItem
func ConvertPbOrderItemToEntity(pbOrderItem *pb_common.OrderItem) (entity.OrderItemInsert, error) {
	oii := entity.OrderItemInsert{}

	if pbOrderItem == nil {
		return oii, fmt.Errorf("pbOrderItem is nil")
	}

	if pbOrderItem.OrderItem == nil {
		return oii, fmt.Errorf("pbOrderItem.OrderItem is nil")
	}

	price, err := decimal.NewFromString(pbOrderItem.ProductPrice)
	if err != nil {
		return oii, fmt.Errorf("error converting price to decimal: %w", err)
	}
	price = price.Round(2)

	salePercentage, err := decimal.NewFromString(pbOrderItem.ProductSalePercentage)
	if err != nil {
		return oii, fmt.Errorf("error converting sale percentage to decimal: %w", err)
	}
	salePercentage = salePercentage.Round(2)

	priceWithSale, err := decimal.NewFromString(pbOrderItem.ProductPriceWithSale)
	if err != nil {
		return oii, fmt.Errorf("error converting price with sale to decimal: %w", err)
	}
	priceWithSale = priceWithSale.Round(2)

	quantity := decimal.NewFromInt32(pbOrderItem.OrderItem.Quantity).Round(0)

	return entity.OrderItemInsert{
		VariantSKU:            pbOrderItem.OrderItem.VariantSku,
		ProductPrice:          price,
		ProductSalePercentage: salePercentage,
		ProductPriceWithSale:  priceWithSale,
		Quantity:              quantity,
	}, nil
}

// RefundReasonKey maps the structured RefundReason enum to its canonical storage key
// (the same buckets the return-analysis chart uses). Returns "" for UNSPECIFIED so the
// caller leaves refund_reason_code NULL and falls back to the free-text reason.
func RefundReasonKey(r pb_admin.RefundReason) string {
	switch r {
	case pb_admin.RefundReason_REFUND_REASON_WRONG_SIZE:
		return "wrong_size"
	case pb_admin.RefundReason_REFUND_REASON_NOT_AS_DESCRIBED:
		return "not_as_described"
	case pb_admin.RefundReason_REFUND_REASON_DEFECTIVE:
		return "defective"
	case pb_admin.RefundReason_REFUND_REASON_CHANGED_MIND:
		return "changed_mind"
	case pb_admin.RefundReason_REFUND_REASON_OTHER:
		return "other"
	default:
		return ""
	}
}

func ConvertCreateCustomOrderRequestToEntity(req *pb_admin.CreateCustomOrderRequest) (*entity.OrderNew, error) {
	if req == nil {
		return nil, fmt.Errorf("create_custom_order_request is nil")
	}
	items := make([]entity.OrderItemInsert, 0, len(req.Items))
	for _, it := range req.Items {
		item, err := ConvertCustomOrderItemInsertToEntity(it, req.Currency)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	buyerVatID, err := convertBuyerVatID(req.BuyerVatId)
	if err != nil {
		return nil, err
	}
	orderNew := &entity.OrderNew{
		Items:             items,
		ShippingAddress:   convertAddress(req.ShippingAddress),
		BillingAddress:    convertAddress(req.BillingAddress),
		Buyer:             convertBuyer(req.Buyer),
		PaymentMethod:     ConvertPbPaymentMethodToEntity(req.PaymentMethod),
		ShipmentCarrierId: int(req.ShipmentCarrierId),
		Currency:          req.Currency,
		BuyerVatID:        buyerVatID,
	}
	if req.ShipmentCost != nil && req.ShipmentCost.GetValue() != "" {
		sc, err := decimal.NewFromString(req.ShipmentCost.GetValue())
		if err != nil {
			return nil, fmt.Errorf("invalid shipment_cost: %w", err)
		}
		if sc.LessThan(decimal.Zero) {
			return nil, fmt.Errorf("shipment_cost must be >= 0")
		}
		sc = RoundForCurrency(sc, req.Currency)
		orderNew.CustomShipmentCost = &sc
	}
	return orderNew, nil
}

func convertBuyer(pb *pb_common.BuyerInsert) *entity.BuyerInsert {
	if pb == nil {
		return nil
	}
	return &entity.BuyerInsert{
		FirstName: pb.FirstName,
		LastName:  pb.LastName,
		Email:     pb.Email,
		Phone:     pb.Phone,
	}
}

// ConvertCustomOrderItemInsertToEntity converts a common.CustomOrderItemInsert to entity.OrderItemInsert.
// Uses custom_price for ProductPrice; ProductSalePercentage is 0, ProductPriceWithSale = ProductPrice.
// custom_price must be strictly positive — the same positive-price invariant standard orders enforce
// (problem 044). A zero/negative price (a silent comp/gift sale) is rejected here; the store re-checks
// via requirePositivePrice as defence in depth. The admin handler maps this error to InvalidArgument.
func ConvertCustomOrderItemInsertToEntity(pb *pb_common.CustomOrderItemInsert, currency string) (entity.OrderItemInsert, error) {
	if pb == nil || pb.CustomPrice == nil {
		return entity.OrderItemInsert{}, fmt.Errorf("custom_order_item_insert: custom_price is required")
	}
	price, err := decimal.NewFromString(pb.CustomPrice.GetValue())
	if err != nil {
		return entity.OrderItemInsert{}, fmt.Errorf("custom_order_item_insert: invalid custom_price: %w", err)
	}
	price = RoundForCurrency(price, currency)
	if price.LessThanOrEqual(decimal.Zero) {
		return entity.OrderItemInsert{}, fmt.Errorf("custom_order_item_insert: custom_price must be positive")
	}
	return entity.OrderItemInsert{
		VariantID:             int(pb.VariantId),
		Quantity:              decimal.NewFromInt32(pb.Quantity).Round(0),
		ProductPrice:          price,
		ProductSalePercentage: decimal.Zero,
		ProductPriceWithSale:  price,
	}, nil
}

// ConvertCommonOrderNewToEntity converts a common.OrderNew to an entity.OrderNew.
func ConvertCommonOrderNewToEntity(commonOrder *pb_common.OrderNew) (*entity.OrderNew, bool) {
	if commonOrder == nil {
		return nil, false
	}

	// Convert items
	var items []entity.OrderItemInsert
	for _, item := range commonOrder.Items {
		newItem := entity.OrderItemInsert{
			VariantSKU: item.VariantSku,
			Quantity:   decimal.NewFromInt32(item.Quantity).Round(0),
		}
		items = append(items, newItem)
	}

	// Convert addresses
	shippingAddress := convertAddress(commonOrder.ShippingAddress)
	billingAddress := convertAddress(commonOrder.BillingAddress)

	// Convert buyer
	var buyer *entity.BuyerInsert
	if commonOrder.Buyer != nil {
		buyer = &entity.BuyerInsert{
			FirstName: commonOrder.Buyer.FirstName,
			LastName:  commonOrder.Buyer.LastName,
			Email:     commonOrder.Buyer.Email,
			Phone:     commonOrder.Buyer.Phone,
		}
	}

	receivePromo := false
	if commonOrder.Buyer != nil {
		receivePromo = commonOrder.Buyer.ReceivePromoEmails
	}
	return &entity.OrderNew{
		Items:             items,
		ShippingAddress:   shippingAddress,
		BillingAddress:    billingAddress,
		Buyer:             buyer,
		PaymentMethod:     ConvertPbPaymentMethodToEntity(commonOrder.PaymentMethod),
		ShipmentCarrierId: int(commonOrder.ShipmentCarrierId),
		PromoCode:         commonOrder.PromoCode,
		Currency:          commonOrder.Currency,
	}, receivePromo
}

// convertAddress converts a common.AddressInsert to an entity.AddressInsert.
func convertAddress(commonAddress *pb_common.AddressInsert) *entity.AddressInsert {
	if commonAddress == nil {
		return nil
	}
	return &entity.AddressInsert{
		Country: commonAddress.Country,
		State: sql.NullString{
			String: commonAddress.State,
			Valid:  commonAddress.State != "",
		},
		City:           commonAddress.City,
		AddressLineOne: commonAddress.AddressLineOne,
		AddressLineTwo: sql.NullString{
			String: commonAddress.AddressLineTwo,
			Valid:  commonAddress.AddressLineTwo != "",
		},
		Company: sql.NullString{
			String: commonAddress.Company,
			Valid:  commonAddress.Company != "",
		},

		PostalCode: commonAddress.PostalCode,
	}
}

func ConvertEntityOrderToPbCommonOrder(eOrder entity.Order) (*pb_common.Order, error) {
	pbOrder := &pb_common.Order{
		Id:             int32(eOrder.Id),
		Uuid:           eOrder.UUID,
		Placed:         timestamppb.New(eOrder.Placed),
		Modified:       timestamppb.New(eOrder.Modified),
		TotalPrice:     &pb_decimal.Decimal{Value: eOrder.TotalPriceDecimal().String()},
		Currency:       eOrder.Currency,
		OrderStatusId:  int32(eOrder.OrderStatusId),
		RefundedAmount: &pb_decimal.Decimal{Value: eOrder.RefundedAmountDecimal().String()},
		RefundReason:   eOrder.RefundReason.String,
		OrderComment:   eOrder.OrderComment.String,
		BuyerEmail:     eOrder.BuyerEmail,
		BuyerFirstName: eOrder.BuyerFirstName,
		BuyerLastName:  eOrder.BuyerLastName,
	}
	if eOrder.PromoId.Valid {
		pbOrder.PromoId = int32(eOrder.PromoId.Int32)
	}
	return pbOrder, nil
}

func ConvertPbOrderItemInsertToEntity(pbOrderItem *pb_common.OrderItemInsert) (*entity.OrderItemInsert, error) {
	if pbOrderItem == nil {
		return nil, fmt.Errorf("pbOrderItem is nil")
	}

	quantityDecimal, err := decimal.NewFromString(fmt.Sprintf("%d", pbOrderItem.Quantity))
	if err != nil {
		return nil, fmt.Errorf("error converting quantity to decimal: %w", err)
	}

	return &entity.OrderItemInsert{
		VariantSKU: pbOrderItem.VariantSku,
		Quantity:   quantityDecimal.Round(0),
	}, nil
}

func ConvertEntityOrderItemInsertToPb(orderItem *entity.OrderItemInsert) *pb_common.OrderItemInsert {
	return &pb_common.OrderItemInsert{
		VariantSku: orderItem.VariantSKU,
		Quantity:   int32(orderItem.Quantity.IntPart()),
	}
}

// ConvertEntityAdjustmentReasonToPb maps entity adjustment reason to proto enum.
func ConvertEntityAdjustmentReasonToPb(reason entity.OrderItemAdjustmentReason) pb_common.OrderItemAdjustmentReasonEnum {
	switch reason {
	case entity.AdjustmentReasonOutOfStock:
		return pb_common.OrderItemAdjustmentReasonEnum_ORDER_ITEM_ADJUSTMENT_REASON_ENUM_OUT_OF_STOCK
	case entity.AdjustmentReasonQuantityReduced:
		return pb_common.OrderItemAdjustmentReasonEnum_ORDER_ITEM_ADJUSTMENT_REASON_ENUM_QUANTITY_REDUCED
	case entity.AdjustmentReasonQuantityCapped:
		return pb_common.OrderItemAdjustmentReasonEnum_ORDER_ITEM_ADJUSTMENT_REASON_ENUM_QUANTITY_CAPPED
	default:
		return pb_common.OrderItemAdjustmentReasonEnum_ORDER_ITEM_ADJUSTMENT_REASON_ENUM_UNKNOWN
	}
}

// ConvertEntityOrderItemAdjustmentsToPb converts entity adjustments to protobuf.
func ConvertEntityOrderItemAdjustmentsToPb(adjustments []entity.OrderItemAdjustment) []*pb_common.OrderItemAdjustment {
	if len(adjustments) == 0 {
		return nil
	}
	pb := make([]*pb_common.OrderItemAdjustment, 0, len(adjustments))
	for _, a := range adjustments {
		pb = append(pb, &pb_common.OrderItemAdjustment{
			VariantSkuSnapshot: a.VariantSKU,
			RequestedQuantity:  &pb_decimal.Decimal{Value: a.RequestedQuantity.String()},
			AdjustedQuantity:   &pb_decimal.Decimal{Value: a.AdjustedQuantity.String()},
			Reason:             ConvertEntityAdjustmentReasonToPb(a.Reason),
		})
	}
	return pb
}

// orderItemSizeName resolves the public size code/name for an order line from the size cache, for the
// GA4 item_variant / display snapshot. Empty if the size is unknown.
func orderItemSizeName(sizeId int) string {
	if sz, ok := cache.GetSizeById(sizeId); ok {
		return sz.Name
	}
	return ""
}

func ConvertEntityOrderItemToPb(orderItem *entity.OrderItem, currency string) *pb_common.OrderItem {
	// Convert translations to protobuf format
	var pbTranslations []*pb_common.ColorwayInsertTranslation
	for _, trans := range orderItem.Translations {
		pbTranslations = append(pbTranslations, &pb_common.ColorwayInsertTranslation{
			LanguageId:  int32(trans.LanguageId),
			Name:        trans.Name,
			Description: trans.Description,
		})
	}

	// orderItem.SKU is already the variant SKU: the snapshot (order_item.variant_sku_snapshot) or, for legacy
	// lines, the live product_size.sku — resolved in the order fetch query. No size suffix to append.
	sku := orderItem.SKU

	return &pb_common.OrderItem{
		Id:                    int32(orderItem.Id),
		OrderId:               int32(orderItem.OrderId),
		Thumbnail:             orderItem.Thumbnail,
		Blurhash:              orderItem.BlurHash,
		ProductPrice:          RoundForCurrency(orderItem.ProductPrice, currency).String(),
		ProductSalePercentage: orderItem.ProductSalePercentageDecimal().String(),
		ProductPriceWithSale:  RoundForCurrency(orderItem.ProductPriceWithSale, currency).String(),
		Slug:                  orderItem.Slug,
		Color:                 orderItem.Color,
		TopCategoryId:         int32(orderItem.TopCategoryId),
		SubCategoryId:         orderItem.SubCategoryId.Int32,
		TypeId:                int32(orderItem.TypeId.Int32),
		ProductBrand:          orderItem.ProductBrand,
		VariantSkuSnapshot:    sku,
		BaseSkuSnapshot:       orderItem.ProductBaseSKU,
		SizeNameSnapshot:      orderItemSizeName(orderItem.SizeId),
		Preorder:              timestamppb.New(orderItem.Preorder.Time),
		OrderItem:             ConvertEntityOrderItemInsertToPb(&orderItem.OrderItemInsert),
		Translations:          pbTranslations,
	}
}

func ConvertEntityOrderFullToPbOrderFull(e *entity.OrderFull) (*pb_common.OrderFull, error) {
	if e == nil {
		return nil, fmt.Errorf("entity.OrderFull is nil")
	}

	pbOrder, err := ConvertEntityOrderToPbCommonOrder(e.Order)
	if err != nil {
		return nil, fmt.Errorf("error converting order: %w", err)
	}

	pbOrderItems, err := ConvertEntityOrderItemsToPbOrderItems(e.OrderItems, e.Order.Currency)
	if err != nil {
		return nil, fmt.Errorf("error converting order items: %w", err)
	}

	pbRefundedOrderItems, err := ConvertEntityOrderItemsToPbOrderItems(e.RefundedOrderItems, e.Order.Currency)
	if err != nil {
		return nil, fmt.Errorf("error converting refunded order items: %w", err)
	}

	pbPayment, err := ConvertEntityToPbPayment(e.Payment)
	if err != nil {
		return nil, fmt.Errorf("error converting payment: %w", err)
	}

	pbShipment, err := ConvertEntityShipmentToPbShipment(e.Shipment)
	if err != nil {
		return nil, fmt.Errorf("error converting shipment: %w", err)
	}

	pbPromoCode := ConvertEntityPromoToPb(e.PromoCode)

	pbBuyer, err := ConvertEntityBuyerToPbBuyer(e.Buyer)
	if err != nil {
		return nil, fmt.Errorf("error converting buyer: %w", err)
	}

	pbBilling, err := ConvertEntityAddressToPbAddress(e.Billing)
	if err != nil {
		return nil, fmt.Errorf("error converting billing address: %w", err)
	}
	pbShipping, err := ConvertEntityAddressToPbAddress(e.Shipping)
	if err != nil {
		return nil, fmt.Errorf("error converting shipping address: %w", err)
	}

	pbStatusHistory := ConvertEntityOrderStatusHistoryToPb(e.StatusHistory)

	out := &pb_common.OrderFull{
		Order:              pbOrder,
		OrderItems:         pbOrderItems,
		RefundedOrderItems: pbRefundedOrderItems,
		Payment:            pbPayment,
		Shipment:           pbShipment,
		PromoCode:          pbPromoCode,
		Buyer:              pbBuyer,
		Billing:            pbBilling,
		Shipping:           pbShipping,
		StatusHistory:      pbStatusHistory,
	}
	if e.OrderReview != nil {
		out.OrderReview = ConvertEntityOrderReviewFullToPb(e.OrderReview)
	}
	return out, nil
}

// stripInternalShipmentCosts blanks the operator-only shipment economics on a converted order:
// actual_cost (the real carrier invoice) and return_shipping_cost (the reverse-logistics cost of a
// return). Both are INTERNAL margin data (#62, base-currency EUR) and must NEVER be exposed to
// storefront customers — only the admin order/fulfillment projections retain them. Nil-safe.
func stripInternalShipmentCosts(o *pb_common.OrderFull) {
	if o == nil || o.Shipment == nil {
		return
	}
	o.Shipment.ActualCost = nil
	o.Shipment.ReturnShippingCost = nil
}

// ConvertEntityOrderFullToPbOrderFullStorefront builds the CUSTOMER-FACING order projection: identical
// to ConvertEntityOrderFullToPbOrderFull but with the internal-only shipment costs stripped
// (stripInternalShipmentCosts). Every storefront (frontend) path that returns an order to a customer
// MUST use this converter — never the admin one — so actual_cost / return_shipping_cost cannot leak
// through the embedded shipment. The admin projection keeps ConvertEntityOrderFullToPbOrderFull so the
// admin order/fulfillment detail still shows them.
func ConvertEntityOrderFullToPbOrderFullStorefront(e *entity.OrderFull) (*pb_common.OrderFull, error) {
	pb, err := ConvertEntityOrderFullToPbOrderFull(e)
	if err != nil {
		return nil, err
	}
	stripInternalShipmentCosts(pb)
	return pb, nil
}

// ConvertEntityOrderStatusHistoryToPb converts entity status history to protobuf
func ConvertEntityOrderStatusHistoryToPb(history []entity.OrderStatusHistoryWithStatus) []*pb_common.OrderStatusHistory {
	result := make([]*pb_common.OrderStatusHistory, len(history))
	for i, h := range history {
		statusEnum, _ := ConvertEntityToPbOrderStatus(h.StatusName)
		result[i] = &pb_common.OrderStatusHistory{
			Id:        int32(h.Id),
			OrderId:   int32(h.OrderId),
			Status:    statusEnum,
			ChangedAt: timestamppb.New(h.ChangedAt),
			ChangedBy: h.ChangedBy.String,
			Notes:     h.Notes.String,
		}
	}
	return result
}

// ConvertEntityOrderItemsToPbOrderItems converts a slice of entity.OrderItem to a slice of pb_common.OrderItem
func ConvertEntityOrderItemsToPbOrderItems(items []entity.OrderItem, currency string) ([]*pb_common.OrderItem, error) {

	pbOrderItems := make([]*pb_common.OrderItem, len(items))
	for i, item := range items {
		pbOrderItems[i] = convertOrderItem(&item, currency)
	}
	return pbOrderItems, nil
}

// convertOrderItem converts an individual entity.OrderItem to a pb_common.OrderItem
func convertOrderItem(e *entity.OrderItem, currency string) *pb_common.OrderItem {
	// Convert translations to protobuf format
	var pbTranslations []*pb_common.ColorwayInsertTranslation
	for _, trans := range e.Translations {
		pbTranslations = append(pbTranslations, &pb_common.ColorwayInsertTranslation{
			LanguageId:  int32(trans.LanguageId),
			Name:        trans.Name,
			Description: trans.Description,
		})
	}

	return &pb_common.OrderItem{
		Id:                    int32(e.Id),
		OrderId:               int32(e.OrderId),
		Thumbnail:             e.Thumbnail,
		Blurhash:              e.BlurHash,
		ProductPrice:          RoundForCurrency(e.ProductPrice, currency).String(),
		ProductPriceWithSale:  RoundForCurrency(e.ProductPriceWithSale, currency).String(),
		ProductSalePercentage: e.ProductSalePercentageDecimal().String(),
		TopCategoryId:         int32(e.TopCategoryId),
		SubCategoryId:         e.SubCategoryId.Int32,
		TypeId:                int32(e.TypeId.Int32),
		ProductBrand:          e.ProductBrand,
		VariantSkuSnapshot:    e.SKU,
		BaseSkuSnapshot:       e.ProductBaseSKU,
		SizeNameSnapshot:      orderItemSizeName(e.SizeId),
		Color:                 e.Color,
		Slug:                  e.Slug,
		Preorder:              timestamppb.New(e.Preorder.Time),
		OrderItem:             convertOrderItemInsert(e.OrderItemInsert),
		Translations:          pbTranslations,
	}
}

// convertOrderItemInsert converts a nested struct or fields of entity.OrderItem to pb_common.OrderItemInsert
func convertOrderItemInsert(e entity.OrderItemInsert) *pb_common.OrderItemInsert {
	return &pb_common.OrderItemInsert{
		VariantSku: e.VariantSKU,
		Quantity:   int32(e.Quantity.IntPart()),
	}
}

func ConvertEntityShipmentToPbShipment(s entity.Shipment) (*pb_common.Shipment, error) {
	return &pb_common.Shipment{
		Cost:                 &pb_decimal.Decimal{Value: s.Cost.String()},
		CreatedAt:            timestamppb.New(s.CreatedAt),
		UpdatedAt:            timestamppb.New(s.UpdatedAt),
		CarrierId:            int32(s.CarrierId),
		TrackingCode:         s.TrackingCode.String,
		ShippingDate:         timestamppb.New(s.ShippingDate.Time),
		EstimatedArrivalDate: timestamppb.New(s.EstimatedArrivalDate.Time),
		FreeShipping:         s.FreeShipping,
		ActualCost:           pbDecimalFromNull(s.ActualCost),
		ReturnShippingCost:   pbDecimalFromNull(s.ReturnShippingCost),
	}, nil
}

// EntityShippingRegionToPb maps entity region string to proto enum
var entityRegionToPb = map[string]pb_common.ShippingRegion{
	string(entity.ShippingRegionAfrica):      pb_common.ShippingRegion_SHIPPING_REGION_AFRICA,
	string(entity.ShippingRegionAmericas):    pb_common.ShippingRegion_SHIPPING_REGION_AMERICAS,
	string(entity.ShippingRegionAsiaPacific): pb_common.ShippingRegion_SHIPPING_REGION_ASIA_PACIFIC,
	string(entity.ShippingRegionEurope):      pb_common.ShippingRegion_SHIPPING_REGION_EUROPE,
	string(entity.ShippingRegionMiddleEast):  pb_common.ShippingRegion_SHIPPING_REGION_MIDDLE_EAST,
}

// PbShippingRegionToEntity maps proto enum to entity region string
var pbRegionToEntity = map[pb_common.ShippingRegion]string{
	pb_common.ShippingRegion_SHIPPING_REGION_AFRICA:       string(entity.ShippingRegionAfrica),
	pb_common.ShippingRegion_SHIPPING_REGION_AMERICAS:     string(entity.ShippingRegionAmericas),
	pb_common.ShippingRegion_SHIPPING_REGION_ASIA_PACIFIC: string(entity.ShippingRegionAsiaPacific),
	pb_common.ShippingRegion_SHIPPING_REGION_EUROPE:       string(entity.ShippingRegionEurope),
	pb_common.ShippingRegion_SHIPPING_REGION_MIDDLE_EAST:  string(entity.ShippingRegionMiddleEast),
}

// ConvertShipmentCarrierRequestToEntity converts request fields to entity.ShipmentCarrierInsert
func ConvertShipmentCarrierRequestToEntity(carrier, trackingURL, description, expectedDeliveryTime, aftershipSlug string, autoDeliverAfterHours int32, allowed bool) entity.ShipmentCarrierInsert {
	slug := strings.TrimSpace(aftershipSlug)
	return entity.ShipmentCarrierInsert{
		Carrier:     strings.TrimSpace(carrier),
		TrackingURL: trackingURL,
		Allowed:     allowed,
		Description: description,
		ExpectedDeliveryTime: sql.NullString{
			String: expectedDeliveryTime,
			Valid:  expectedDeliveryTime != "",
		},
		AftershipSlug: sql.NullString{
			String: slug,
			Valid:  slug != "",
		},
		AutoDeliverAfterHours: int(autoDeliverAfterHours),
	}
}

// ConvertPbShippingRegionsToEntity converts proto enum slice to entity region strings (skips UNKNOWN)
func ConvertPbShippingRegionsToEntity(pbRegions []pb_common.ShippingRegion) []string {
	out := make([]string, 0, len(pbRegions))
	for _, r := range pbRegions {
		if r == pb_common.ShippingRegion_SHIPPING_REGION_UNKNOWN {
			continue
		}
		if s, ok := pbRegionToEntity[r]; ok {
			out = append(out, s)
		}
	}
	return out
}

func ConvertEntityShipmentCarrierToPbShipmentCarrier(s *entity.ShipmentCarrier) (*pb_common.ShipmentCarrier, error) {
	if s == nil {
		return nil, fmt.Errorf("empty entity.ShipmentCarrier")
	}

	// Convert prices to protobuf format
	pbPrices := make([]*pb_common.ShipmentCarrierPrice, 0, len(s.Prices))
	for _, price := range s.Prices {
		pbPrices = append(pbPrices, &pb_common.ShipmentCarrierPrice{
			Currency: price.Currency,
			Price: &pb_decimal.Decimal{
				Value: price.Price.String(),
			},
		})
	}

	// Convert allowed regions to proto enum
	pbRegions := make([]pb_common.ShippingRegion, 0, len(s.AllowedRegions))
	for _, r := range s.AllowedRegions {
		if e, ok := entityRegionToPb[r]; ok {
			pbRegions = append(pbRegions, e)
		}
	}

	expectedDeliveryTime := ""
	if s.ExpectedDeliveryTime.Valid {
		expectedDeliveryTime = s.ExpectedDeliveryTime.String
	}
	aftershipSlug := ""
	if s.AftershipSlug.Valid {
		aftershipSlug = s.AftershipSlug.String
	}
	return &pb_common.ShipmentCarrier{
		Id: int32(s.Id),
		ShipmentCarrier: &pb_common.ShipmentCarrierInsert{
			Carrier:               s.Carrier,
			Allowed:               s.Allowed,
			Description:           s.Description,
			TrackingUrl:           s.TrackingURL,
			ExpectedDeliveryTime:  expectedDeliveryTime,
			AftershipSlug:         aftershipSlug,
			AutoDeliverAfterHours: int32(s.AutoDeliverAfterHours),
		},
		Prices:         pbPrices,
		AllowedRegions: pbRegions,
	}, nil
}

func ConvertEntityBuyerToPbBuyer(b entity.Buyer) (*pb_common.Buyer, error) {

	return &pb_common.Buyer{
		BuyerInsert: &pb_common.BuyerInsert{
			FirstName:          b.FirstName,
			LastName:           b.LastName,
			Email:              b.Email,
			Phone:              b.Phone,
			ReceivePromoEmails: b.ReceivePromoEmails.Bool,
		},
	}, nil
}

func ConvertEntityAddressToPbAddress(a entity.Address) (*pb_common.Address, error) {
	return &pb_common.Address{
		AddressInsert: &pb_common.AddressInsert{
			Country:        a.Country,
			State:          a.State.String,
			City:           a.City,
			AddressLineOne: a.AddressLineOne,
			AddressLineTwo: a.AddressLineTwo.String,
			Company:        a.Company.String,
			PostalCode:     a.PostalCode,
		},
	}, nil
}

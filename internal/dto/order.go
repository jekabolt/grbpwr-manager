// Package dto contains data transfer objects for orders.
package dto

import (
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConvertPbOrderItemToEntity converts a protobuf OrderItem to an entity OrderItem
func ConvertPbOrderItemToEntity(pbOrderItem *pb_common.OrderItem) (entity.OrderItemInsert, error) {

	oii := entity.OrderItemInsert{}

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
		ProductId:             int(pbOrderItem.OrderItem.ProductId),
		ProductPrice:          price,
		ProductSalePercentage: salePercentage,
		ProductPriceWithSale:  priceWithSale,
		Quantity:              quantity,
		SizeId:                int(pbOrderItem.OrderItem.SizeId),
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
			ProductId: int(item.ProductId),
			Quantity:  decimal.NewFromInt32(item.Quantity).Round(0),
			SizeId:    int(item.SizeId),
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

	return &entity.OrderNew{
		Items:             items,
		ShippingAddress:   shippingAddress,
		BillingAddress:    billingAddress,
		Buyer:             buyer,
		PaymentMethod:     ConvertPbPaymentMethodToEntity(commonOrder.PaymentMethod),
		ShipmentCarrierId: int(commonOrder.ShipmentCarrierId),
		PromoCode:         commonOrder.PromoCode,
		Currency:          commonOrder.Currency,
	}, commonOrder.Buyer.ReceivePromoEmails
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
		Id:            int32(eOrder.Id),
		Uuid:          eOrder.UUID,
		Placed:        timestamppb.New(eOrder.Placed),
		Modified:      timestamppb.New(eOrder.Modified),
		TotalPrice:    &pb_decimal.Decimal{Value: eOrder.TotalPriceDecimal().String()},
		Currency:      eOrder.Currency,
		OrderStatusId: int32(eOrder.OrderStatusId),
	}

	if eOrder.PromoId.Valid {
		pbOrder.PromoId = int32(eOrder.PromoId.Int32)
	}

	if eOrder.RefundReason.Valid {
		pbOrder.RefundReason = eOrder.RefundReason.String
	}

	if eOrder.OrderComment.Valid {
		pbOrder.OrderComment = eOrder.OrderComment.String
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
		ProductId: int(pbOrderItem.ProductId),
		Quantity:  quantityDecimal.Round(0),
		SizeId:    int(pbOrderItem.SizeId),
	}, nil
}

func ConvertEntityOrderItemInsertToPb(orderItem *entity.OrderItemInsert) *pb_common.OrderItemInsert {
	return &pb_common.OrderItemInsert{
		ProductId: int32(orderItem.ProductId),
		Quantity:  int32(orderItem.Quantity.IntPart()),
		SizeId:    int32(orderItem.SizeId),
	}
}

func ConvertEntityOrderItemToPb(orderItem *entity.OrderItem) *pb_common.OrderItem {
	// Convert translations to protobuf format
	var pbTranslations []*pb_common.ProductInsertTranslation
	for _, trans := range orderItem.Translations {
		pbTranslations = append(pbTranslations, &pb_common.ProductInsertTranslation{
			LanguageId:  int32(trans.LanguageId),
			Name:        trans.Name,
			Description: trans.Description,
		})
	}

	return &pb_common.OrderItem{
		Id:                    int32(orderItem.Id),
		OrderId:               int32(orderItem.OrderId),
		Thumbnail:             orderItem.Thumbnail,
		Blurhash:              orderItem.BlurHash,
		ProductPrice:          orderItem.ProductPriceDecimal().String(),
		ProductSalePercentage: orderItem.ProductSalePercentageDecimal().String(),
		ProductPriceWithSale:  orderItem.ProductPriceWithSaleDecimal().String(),
		Slug:                  orderItem.Slug,
		Color:                 orderItem.Color,
		TopCategoryId:         int32(orderItem.TopCategoryId),
		SubCategoryId:         orderItem.SubCategoryId.Int32,
		TypeId:                int32(orderItem.TypeId.Int32),
		ProductBrand:          orderItem.ProductBrand,
		Sku:                   orderItem.SKU,
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

	pbOrderItems, err := ConvertEntityOrderItemsToPbOrderItems(e.OrderItems)
	if err != nil {
		return nil, fmt.Errorf("error converting order items: %w", err)
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

	return &pb_common.OrderFull{
		Order:      pbOrder,
		OrderItems: pbOrderItems,
		Payment:    pbPayment,
		Shipment:   pbShipment,
		PromoCode:  pbPromoCode,
		Buyer:      pbBuyer,
		Billing:    pbBilling,
		Shipping:   pbShipping,
	}, nil
}

// ConvertEntityOrderItemsToPbOrderItems converts a slice of entity.OrderItem to a slice of pb_common.OrderItem
func ConvertEntityOrderItemsToPbOrderItems(items []entity.OrderItem) ([]*pb_common.OrderItem, error) {

	pbOrderItems := make([]*pb_common.OrderItem, len(items))
	for i, item := range items {
		pbOrderItems[i] = convertOrderItem(&item)
	}
	return pbOrderItems, nil
}

// convertOrderItem converts an individual entity.OrderItem to a pb_common.OrderItem
func convertOrderItem(e *entity.OrderItem) *pb_common.OrderItem {
	// Convert translations to protobuf format
	var pbTranslations []*pb_common.ProductInsertTranslation
	for _, trans := range e.Translations {
		pbTranslations = append(pbTranslations, &pb_common.ProductInsertTranslation{
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
		ProductPrice:          e.ProductPriceDecimal().String(),
		ProductPriceWithSale:  e.ProductPriceWithSaleDecimal().String(),
		ProductSalePercentage: e.ProductSalePercentageDecimal().String(),
		TopCategoryId:         int32(e.TopCategoryId),
		SubCategoryId:         e.SubCategoryId.Int32,
		TypeId:                int32(e.TypeId.Int32),
		ProductBrand:          e.ProductBrand,
		Sku:                   e.SKU,
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
		ProductId: int32(e.ProductId),
		Quantity:  int32(e.Quantity.IntPart()),
		SizeId:    int32(e.SizeId),
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
	}, nil
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

	return &pb_common.ShipmentCarrier{
		Id: int32(s.Id),
		ShipmentCarrier: &pb_common.ShipmentCarrierInsert{
			Carrier:     s.Carrier,
			Allowed:     s.Allowed,
			Description: s.Description,
		},
		Prices: pbPrices,
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

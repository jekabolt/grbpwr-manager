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
func ConvertPbOrderItemToEntity(pbOrderItem *pb_common.OrderItem) entity.OrderItemInsert {
	return entity.OrderItemInsert{
		ProductID: int(pbOrderItem.OrderItem.ProductId),
		Quantity:  decimal.NewFromInt32(pbOrderItem.OrderItem.Quantity),
		SizeID:    int(pbOrderItem.OrderItem.SizeId),
	}
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
			ProductID: int(item.ProductId),
			Quantity:  decimal.NewFromInt32(item.Quantity),
			SizeID:    int(item.SizeId),
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

func ConvertEntityOrderToPbCommonOrder(eOrder *entity.Order) (*pb_common.Order, error) {
	if eOrder == nil {
		return nil, fmt.Errorf("order is nil")
	}

	pbOrder := &pb_common.Order{
		Id:            int32(eOrder.ID),
		Uuid:          eOrder.UUID,
		BuyerId:       int32(eOrder.BuyerID),
		Placed:        timestamppb.New(eOrder.Placed),
		Modified:      timestamppb.New(eOrder.Modified),
		PaymentId:     int32(eOrder.PaymentID),
		TotalPrice:    &pb_decimal.Decimal{Value: eOrder.TotalPrice.String()},
		OrderStatusId: int32(eOrder.OrderStatusID),
		ShipmentId:    int32(eOrder.ShipmentId),
	}

	if eOrder.PromoID.Valid {
		pbOrder.PromoId = int32(eOrder.PromoID.Int32)
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
		ProductID: int(pbOrderItem.ProductId),
		Quantity:  quantityDecimal,
		SizeID:    int(pbOrderItem.SizeId),
	}, nil
}

func ConvertEntityOrderItemInsertToPb(orderItem *entity.OrderItemInsert) *pb_common.OrderItemInsert {
	return &pb_common.OrderItemInsert{
		ProductId: int32(orderItem.ProductID),
		Quantity:  int32(orderItem.Quantity.IntPart()),
		SizeId:    int32(orderItem.SizeID),
	}
}

func ConvertEntityOrderItemToPb(orderItem *entity.OrderItem) *pb_common.OrderItem {
	return &pb_common.OrderItem{
		Id:                    int32(orderItem.ID),
		OrderId:               int32(orderItem.OrderID),
		Thumbnail:             orderItem.Thumbnail,
		ProductName:           orderItem.ProductName,
		ProductPrice:          orderItem.ProductPrice.String(),
		ProductSalePercentage: orderItem.ProductSalePercentage.String(),
		ProductPriceWithSale:  orderItem.ProductPriceWithSale.String(),
		Slug:                  orderItem.Slug,
		Color:                 orderItem.Color,
		CategoryId:            int32(orderItem.CategoryID),
		ProductBrand:          orderItem.ProductBrand,
		Sku:                   orderItem.SKU,
		OrderItem:             ConvertEntityOrderItemInsertToPb(&orderItem.OrderItemInsert),
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
	// Replace the following with actual conversion logic based on the structure of entity.OrderItem
	return &pb_common.OrderItem{
		Id:                    int32(e.ID),
		OrderId:               int32(e.OrderID),
		Thumbnail:             e.Thumbnail,
		ProductName:           e.ProductName,
		ProductPrice:          e.ProductPrice.String(),
		ProductSalePercentage: e.ProductSalePercentage.String(),
		CategoryId:            int32(e.CategoryID),
		ProductBrand:          e.ProductBrand,
		Sku:                   e.SKU,
		// Assuming OrderItem has a nested struct or fields that can be mapped to OrderItemInsert
		OrderItem: convertOrderItemInsert(e.OrderItemInsert),
	}
}

// convertOrderItemInsert converts a nested struct or fields of entity.OrderItem to pb_common.OrderItemInsert
func convertOrderItemInsert(e entity.OrderItemInsert) *pb_common.OrderItemInsert {
	return &pb_common.OrderItemInsert{
		ProductId: int32(e.ProductID),
		Quantity:  int32(e.Quantity.IntPart()),
		SizeId:    int32(e.SizeID),
	}
}

func ConvertEntityShipmentToPbShipment(s *entity.Shipment) (*pb_common.Shipment, error) {
	if s == nil {
		return nil, fmt.Errorf("empty entity.Shipment")
	}

	return &pb_common.Shipment{
		Id:                   int32(s.ID),
		CreatedAt:            timestamppb.New(s.CreatedAt),
		UpdatedAt:            timestamppb.New(s.UpdatedAt),
		CarrierId:            int32(s.CarrierID),
		TrackingCode:         s.TrackingCode.String,
		ShippingDate:         timestamppb.New(s.ShippingDate.Time),
		EstimatedArrivalDate: timestamppb.New(s.EstimatedArrivalDate.Time),
	}, nil
}

func ConvertEntityShipmentCarrierToPbShipmentCarrier(s *entity.ShipmentCarrier) (*pb_common.ShipmentCarrier, error) {
	if s == nil {
		return nil, fmt.Errorf("empty entity.ShipmentCarrier")
	}

	return &pb_common.ShipmentCarrier{
		Id: int32(s.ID),
		ShipmentCarrier: &pb_common.ShipmentCarrierInsert{
			Carrier: s.Carrier,
			Price:   &pb_decimal.Decimal{Value: s.Price.String()},
			Allowed: s.Allowed,
		},
	}, nil
}

func ConvertEntityBuyerToPbBuyer(b *entity.Buyer) (*pb_common.Buyer, error) {
	if b == nil {
		return nil, fmt.Errorf("empty entity.Buyer")
	}

	return &pb_common.Buyer{
		Id:                int32(b.ID),
		BillingAddressId:  int32(b.BillingAddressID),
		ShippingAddressId: int32(b.ShippingAddressID),
		BuyerInsert: &pb_common.BuyerInsert{
			FirstName:          b.FirstName,
			LastName:           b.LastName,
			Email:              b.Email,
			Phone:              b.Phone,
			ReceivePromoEmails: b.ReceivePromoEmails.Bool,
		},
	}, nil
}

func ConvertEntityAddressToPbAddress(a *entity.Address) (*pb_common.Address, error) {
	if a == nil {
		return nil, fmt.Errorf("empty entity.Address")
	}

	return &pb_common.Address{
		Id: int32(a.ID),
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

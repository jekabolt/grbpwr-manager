// Package dto contains data transfer objects for orders.
package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
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
func ConvertCommonOrderNewToEntity(commonOrder *pb_common.OrderNew) *entity.OrderNew {
	if commonOrder == nil {
		return nil
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
			FirstName:          commonOrder.Buyer.FirstName,
			LastName:           commonOrder.Buyer.LastName,
			Email:              commonOrder.Buyer.Email,
			Phone:              commonOrder.Buyer.Phone,
			ReceivePromoEmails: commonOrder.Buyer.ReceivePromoEmails,
		}
	}

	return &entity.OrderNew{
		Items:             items,
		ShippingAddress:   shippingAddress,
		BillingAddress:    billingAddress,
		Buyer:             buyer,
		PaymentMethodId:   int(commonOrder.PaymentMethodId),
		ShipmentCarrierId: int(commonOrder.ShipmentCarrierId),
		PromoCode:         commonOrder.PromoCode,
	}
}

// convertAddress converts a common.AddressInsert to an entity.AddressInsert.
func convertAddress(commonAddress *pb_common.AddressInsert) *entity.AddressInsert {
	if commonAddress == nil {
		return nil
	}
	return &entity.AddressInsert{
		Street:          commonAddress.Street,
		HouseNumber:     commonAddress.HouseNumber,
		ApartmentNumber: commonAddress.ApartmentNumber,
		City:            commonAddress.City,
		State:           commonAddress.State,
		Country:         commonAddress.Country,
		PostalCode:      commonAddress.PostalCode,
	}
}

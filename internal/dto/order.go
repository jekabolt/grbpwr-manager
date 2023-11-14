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

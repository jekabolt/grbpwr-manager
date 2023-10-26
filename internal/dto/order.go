// Package dto contains data transfer objects for orders.
package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ConvertPbOrderItemToEntity converts a protobuf OrderItem to an entity OrderItem
func ConvertPbOrderItemToEntity(pbOrderItem *pb_common.OrderItem) entity.OrderItem {
	return entity.OrderItem{
		ID:        int(pbOrderItem.Id),
		OrderID:   int(pbOrderItem.OrderId),
		ProductID: int(pbOrderItem.ProductId),
		Quantity:  int(pbOrderItem.Quantity),
		SizeID:    int(pbOrderItem.SizeId),
	}
}

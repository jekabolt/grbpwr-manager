package form

import (
	"fmt"

	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

type ReduceStockForProductSizesRequest struct {
	*pb_admin.ReduceStockForProductSizesRequest
}

func (r *ReduceStockForProductSizesRequest) Validate() error {
	return ValidateStruct(r,
		v.Field(&r.Items, v.Required, v.Length(1, 100), v.By(validateOrderItems)),
	)
}

func validateOrderItems(value interface{}) error {
	items, ok := value.([]*pb_common.OrderItem)
	if !ok {
		return fmt.Errorf("invalid type for OrderItems")
	}

	for _, item := range items {
		err := ValidateStruct(&OrderItemWrapper{item},
			v.Field(&item.Id, v.Required, v.Min(1)),
			v.Field(&item.OrderId, v.Required, v.Min(1)),
			v.Field(&item.ProductId, v.Required, v.Min(1)),
			v.Field(&item.Quantity, v.Required, v.Min(1)),
			v.Field(&item.SizeId, v.Required, v.Min(1)),
		)
		if err != nil {
			return err
		}
	}
	return nil
}

type OrderItemWrapper struct {
	*pb_common.OrderItem
}

func (i *OrderItemWrapper) Validate() error {
	return ValidateStruct(i,
		v.Field(&i.Id, v.Required, v.Min(1)),
		v.Field(&i.OrderId, v.Required, v.Min(1)),
		v.Field(&i.ProductId, v.Required, v.Min(1)),
		v.Field(&i.Quantity, v.Required, v.Min(1)),
		v.Field(&i.SizeId, v.Required, v.Min(1)),
	)
}

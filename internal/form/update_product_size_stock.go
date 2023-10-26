package form

import (
	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

type UpdateProductSizeStockRequest struct {
	*pb_admin.UpdateProductSizeStockRequest
}

func (r *UpdateProductSizeStockRequest) Validate() error {
	return v.ValidateStruct(r,
		v.Field(&r.ProductId, v.Required, v.Min(1)),
		v.Field(&r.SizeId, v.Required, v.Min(1)),
		v.Field(&r.Quantity, v.Required, v.Min(0)),
	)
}

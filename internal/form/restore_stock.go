package form

import (
	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

type RestoreStockForProductSizesRequest struct {
	*pb_admin.RestoreStockForProductSizesRequest
}

func (r *RestoreStockForProductSizesRequest) Validate() error {
	return v.ValidateStruct(r,
		v.Field(&r.Items, v.Required, v.Length(1, 100), v.By(validateOrderItems)),
	)
}

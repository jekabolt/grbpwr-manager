package form

import (
	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

type UpdateProductNameRequest struct {
	*pb_admin.UpdateProductNameRequest
}

func (r *UpdateProductNameRequest) Validate() error {
	return v.ValidateStruct(r,
		v.Field(&r.ProductID, v.Required, v.Min(1)),
		v.Field(&r.Name, v.Required, v.Length(1, 100)),
	)
}

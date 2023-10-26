package form

import (
	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

type GetProductByIDRequest struct {
	*pb_admin.GetProductByIDRequest
}

func (r *GetProductByIDRequest) Validate() error {
	return ValidateStruct(r,
		v.Field(&r.Id, v.Required, v.Min(1)),
	)
}

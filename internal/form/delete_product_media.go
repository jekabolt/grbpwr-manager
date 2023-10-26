package form

import (
	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

type DeleteProductMediaRequest struct {
	*pb_admin.DeleteProductMediaRequest
}

func (r *DeleteProductMediaRequest) Validate() error {
	return ValidateStruct(r,
		v.Field(&r.ProductMediaId, v.Required, v.Min(1)),
	)
}

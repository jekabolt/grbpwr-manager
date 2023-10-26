package form

import (
	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

type DeleteProductTagRequest struct {
	*pb_admin.DeleteProductTagRequest
}

func (r *DeleteProductTagRequest) Validate() error {
	return ValidateStruct(r,
		v.Field(&r.ProductId, v.Required, v.Min(1)),
		v.Field(&r.Tag, v.Required, v.Length(1, 100)),
	)
}

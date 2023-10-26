package form

import (
	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

type AddProductMediaRequest struct {
	*pb_admin.AddProductMediaRequest
}

func (r *AddProductMediaRequest) Validate() error {
	return ValidateStruct(r,
		v.Field(&r.ProductId, v.Required, v.Min(1)),
		v.Field(&r.FullSize, v.Required),
		v.Field(&r.Thumbnail, v.Required),
		v.Field(&r.Compressed, v.Required),
	)
}

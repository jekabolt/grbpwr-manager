package form

import (
	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

type UpdateProductThumbnailRequest struct {
	*pb_admin.UpdateProductThumbnailRequest
}

func (r *UpdateProductThumbnailRequest) Validate() error {
	return v.ValidateStruct(r,
		v.Field(&r.ProductID, v.Required, v.Min(1)),
		v.Field(&r.Thumbnail, v.Required, v.Length(1, 256)),
	)
}

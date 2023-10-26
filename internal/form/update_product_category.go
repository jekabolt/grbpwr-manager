package form

import (
	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

type UpdateProductCategoryRequest struct {
	*pb_admin.UpdateProductCategoryRequest
}

func (r *UpdateProductCategoryRequest) Validate() error {
	return v.ValidateStruct(r,
		v.Field(&r.ProductID, v.Required, v.Min(1)),
		v.Field(&r.CategoryID, v.Required, v.Min(1)),
	)
}

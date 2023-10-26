package form

import (
	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

type DeleteProductMeasurementRequest struct {
	*pb_admin.DeleteProductMeasurementRequest
}

func (r *DeleteProductMeasurementRequest) Validate() error {
	return ValidateStruct(r,
		v.Field(&r.Id, v.Required, v.Min(1)),
	)
}
